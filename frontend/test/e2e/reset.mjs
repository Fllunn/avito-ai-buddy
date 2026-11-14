import { request } from 'playwright';
import fs from 'node:fs/promises';
import assert from 'node:assert/strict';
import {execFileSync} from 'node:child_process';
const project='ai-buddy-acceptance-20261114';
const envFile=process.env.BUDDY_ENV_FILE??'/private/tmp/buddy-acceptance.env';
const base='http://localhost:15173',panel='http://localhost:18081';
const env=Object.fromEntries((await fs.readFile(envFile,'utf8')).split('\n').filter(x=>x.includes('=')).map(x=>[x.slice(0,x.indexOf('=')),x.slice(x.indexOf('=')+1)]));
const args=['compose','-p',project,'--env-file',envFile,'-f','compose.yaml','-f','infra/acceptance.compose.yaml'];
const docker=(...a)=>execFileSync('docker',[...args,...a],{encoding:'utf8',stdio:['pipe','pipe','pipe']});
const sql=(db,q)=>execFileSync('docker',[...args,'exec','-T','postgres','sh','-c','PGPASSWORD="$POSTGRES_PASSWORD" psql -X -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d '+db+' -At'],{input:q,encoding:'utf8'}).trim();
const ctx=await request.newContext();
const api=async(path,method='GET',data)=>{const r=await ctx.fetch(base+'/api'+path,{method,data,headers:{Origin:base}});assert(r.ok(),`${path}: ${r.status()}`);return r.status()===204?null:r.json();};
try {
 await api('/demo/session','POST',{user_id:'alina'});
 let r=await ctx.post(panel+'/demo/session',{data:{secret:env.DEMO_ADMIN_SECRET},headers:{Origin:panel}});assert(r.ok());
 const draft=(await api('/cards','POST',{type:'access_create',parameters:{resource_id:'monitoring',level:'read',justification:'Для проверки согласованного сброса'}})).card;
 r=await ctx.put(panel+'/demo/faults/access',{data:{mode:'lose_response_after',delay_ms:0},headers:{Origin:panel}});assert(r.ok());
 const result=await api(`/cards/${draft.id}/confirm`,'POST',{expected_version:draft.version});
 let unknown=false;
 for(let i=0;i<150;i++){const a=await api(`/attempts/${result.attempt_id}`);if(a.state==='unknown'){unknown=true;break;}await new Promise(r=>setTimeout(r,30));}
 assert(unknown,'unknown observed');
 docker('stop','api');
 // Deterministic queued fixture after workers stop: no external send can race setup.
 sql('buddy_api',`UPDATE attempts SET state='unknown' WHERE id='${result.attempt_id}'; UPDATE jobs SET state='queued',lease_token=NULL,lease_until=NULL,kind='reconcile' WHERE attempt_id='${result.attempt_id}' AND state<>'done';
 INSERT INTO cards(id,user_id,type,version,state,expires_at,updated_at) VALUES('11111111-1111-4111-8111-111111111111','alina','access_create',1,'confirmed',now()+interval '1 day',now());
 INSERT INTO card_versions(card_id,version,parameters,expires_at,created_at) VALUES('11111111-1111-4111-8111-111111111111',1,'{"resource_id":"repo-payments","level":"read","justification":"Для проверки сброса очереди"}',now()+interval '1 day',now());
 INSERT INTO attempts(id,user_id,card_id,card_version,number,operation_key,parameters,state,conflict_key,created_at,updated_at) SELECT '22222222-2222-4222-8222-222222222222','alina',card_id,version,1,'33333333-3333-4333-8333-333333333333',parameters,'queued','resource:repo-payments',now(),now() FROM card_versions WHERE card_id='11111111-1111-4111-8111-111111111111';
 INSERT INTO jobs(id,user_id,attempt_id,kind,state,available_at,created_at) VALUES('44444444-4444-4444-8444-444444444444','alina','22222222-2222-4222-8222-222222222222','send','queued',now(),now());`);
 assert.equal(sql('buddy_api',"SELECT count(*) FROM attempts WHERE state IN ('unknown','queued')"),'2');
 let failed=false;try{execFileSync('./scripts/demo-reset.sh',['demo'],{env:{...process.env,DEMO_RESET_PROJECT:project,DEMO_RESET_ACCEPTANCE:'1',DEMO_RESET_ENV_FILE:envFile,DEMO_RESET_TEST_FAIL_AFTER:'api'},stdio:'pipe'});}catch{failed=true;}assert(failed);
 const states=docker('ps','-a','--format','json').trim().split('\n').map(x=>JSON.parse(x));
 assert(states.filter(x=>['api','mocks','frontend'].includes(x.Service)).every(x=>x.State==='exited'));
 execFileSync('./scripts/demo-reset.sh',['demo'],{env:{...process.env,DEMO_RESET_PROJECT:project,DEMO_RESET_ACCEPTANCE:'1',DEMO_RESET_ENV_FILE:envFile},stdio:'pipe'});
 await new Promise(r=>setTimeout(r,4500));
 assert.equal(sql('buddy_api','SELECT count(*) FROM attempts'),'0');
 assert.equal(sql('buddy_api',"SELECT count(*) FROM jobs WHERE kind IN ('send','reconcile')"),'0');
 assert.equal(sql('buddy_mocks','SELECT count(*) FROM operations'),'0');
 assert.equal((await ctx.get(base+'/api/me')).status(),401);
 await fs.writeFile('docs/verification/reset.json',JSON.stringify({date:'2026-11-14',project,mode:'fake',checkpoints:['unknown from actual lost response and deterministic queued job before reset','partial failure stops all handlers','retry completes both database and cache reset','old attempts, jobs and operation keys absent after waiting','old cookie rejected'],late_operations:0},null,2));
 console.log('reset: unknown/queued, partial failure, retry and no late sends passed');
} finally {await ctx.dispose();}
