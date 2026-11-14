import {request} from 'playwright';import fs from 'node:fs/promises';import assert from 'node:assert/strict';import {execFileSync} from 'node:child_process';
const base='http://localhost:25173';const project='ai-buddy-redesign-final-20261114';const envFile=process.env.BUDDY_ENV_FILE??'.env.redesign.local';
const ctx=await request.newContext();
const get=async(path)=>{const r=await ctx.get(base+'/api'+path);assert(r.ok());return r.json();};
const compose=(...args)=>execFileSync('docker',['compose','-p',project,'--env-file',envFile,'-f','compose.yaml','-f','infra/redesign.compose.yaml',...args],{stdio:'pipe'});
try{
 const r=await ctx.post(base+'/api/demo/session',{data:{user_id:'alina'},headers:{Origin:base}});assert(r.ok());
 const before=await get('/onboarding');const history=await get('/chat');const routes=await get('/executions');const stable=routes.items.find(r=>r.state==='completed');const saved=stable?await get(`/executions/${stable.id}?limit=100`):null;
 compose('stop','frontend','api','mocks');compose('up','-d','--wait','--wait-timeout','60');
 const after=await get('/onboarding');const restored=await get('/chat');if(saved){const route=await get(`/executions/${stable.id}?limit=100`);assert.equal(route.snapshot.revision,saved.snapshot.revision);assert.equal(route.snapshot.last_sequence,saved.snapshot.last_sequence);assert.deepEqual(route.events,saved.events);}
 const objects=(v)=>v.requests.map(o=>[o.id,o.status,o.version]).sort();
 assert.deepEqual(objects(after),objects(before));for(const message of history.items) {assert(restored.items.some(item=>item.id===message.id && item.text===message.text));}
 await fs.writeFile('docs/verification/redesign/restart.json',JSON.stringify({date:'2026-11-14',mode:'fake',project,preserved_objects:objects(after),session_restored:true,history_preserved:true,execution_events_preserved:!!saved},null,2));console.log('restart: session, history and service object versions preserved');
}finally{await ctx.dispose();}
