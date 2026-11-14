import { chromium } from 'playwright';
import fs from 'node:fs/promises';
import assert from 'node:assert/strict';
const base = process.env.BUDDY_BASE_URL ?? 'http://localhost:15173';
const panel = process.env.BUDDY_PANEL_URL ?? 'http://localhost:18081';
const env = Object.fromEntries((await fs.readFile(process.env.BUDDY_ENV_FILE ?? '/private/tmp/buddy-acceptance.env','utf8')).split('\n').filter((v)=>v.includes('=')).map((v)=>[v.slice(0,v.indexOf('=')),v.slice(v.indexOf('=')+1)]));
const browser = await chromium.launch({headless:true});const context = await browser.newContext({viewport:{width:1440,height:1000}});const page = await context.newPage();
const checkpoints=[];
const pause=(ms)=>new Promise((resolve)=>setTimeout(resolve,ms));
async function api(path,method='GET',data){const r=await context.request.fetch(`${base}/api${path}`,{method,data,headers:{Origin:base}});const body=r.status()===204?null:await r.json();assert(r.ok(),`${path}: ${r.status()} ${JSON.stringify(body)}`);return body;}
async function fault(mode,delay_ms=0){const r=await context.request.put(`${panel}/demo/faults/access`,{data:{mode,delay_ms},headers:{Origin:panel}});assert(r.ok());}
async function poll(check,label,timeout=20000){const start=Date.now();while(Date.now()-start<timeout){const value=await check();if(value)return value;await pause(80);}throw new Error(`Timed out: ${label}`);}
async function create(resource){return (await api('/cards','POST',{type:'access_create',parameters:{resource_id:resource,level:'read',justification:'Для исследования демонстрационного сервиса'}})).card;}
async function confirm(card){return api(`/cards/${card.id}/confirm`,'POST',{expected_version:card.version});}
try{
 await api('/demo/session','POST',{user_id:'alina'});
 let r=await context.request.post(`${panel}/demo/session`,{data:{secret:env.DEMO_ADMIN_SECRET},headers:{Origin:panel}});assert(r.ok());
 await page.goto(base);await page.getByRole('heading',{name:'Привет, Алина!'}).waitFor();
 const slow=await create('monitoring');await fault('delay_before',2000);const queued=await confirm(slow);
 const start=Date.now();await api('/onboarding');assert(Date.now()-start<1000);
 await page.reload();await page.getByLabel('Сообщение AI-buddy').waitFor();assert(await page.getByLabel('Сообщение AI-buddy').isEnabled());await page.getByLabel('Сообщение AI-buddy').fill('Интерфейс остаётся доступным');
 await poll(async()=>{const a=await api(`/attempts/${queued.attempt_id}`);return a.state==='succeeded';},'delayed service');await fault('normal');checkpoints.push('2 second service delay does not block plan or composer');
 const unavailable=await create('repo-payments');await fault('unavailable_before');const u=await confirm(unavailable);
 await poll(async()=>{const a=await api(`/attempts/${u.attempt_id}`);return a.state==='unavailable';},'guaranteed unavailable');
 await fault('normal');const retry=await api(`/attempts/${u.attempt_id}/retry`,'POST',{});assert.equal(retry.attempt_id,u.attempt_id);
 await poll(async()=>{const a=await api(`/attempts/${u.attempt_id}`);return a.state==='succeeded';},'same-key retry');checkpoints.push('guaranteed unavailable and retry use the same attempt');
 const lost=await create('product-metrics');await fault('lose_response_after');const l=await confirm(lost);
 await poll(async()=>{const a=await api(`/attempts/${l.attempt_id}`);return a.state==='unknown';},'unknown outcome');
 const conflict=await context.request.post(`${base}/api/cards`,{data:{type:'access_create',parameters:{resource_id:'product-metrics',level:'read',justification:'Для исследования демонстрационного сервиса'}},headers:{Origin:base}});assert.equal(conflict.status(),409);
 await fault('normal');await poll(async()=>{const a=await api(`/attempts/${l.attempt_id}`);return a.state==='succeeded';},'reconciled success');
 const serviceList=await context.request.get(`${panel}/v1/access-requests`,{headers:{Authorization:`Bearer ${env.SERVICE_SECRET}`,'X-Actor-ID':'alina'}});const objects=await serviceList.json();assert.equal(objects.items.filter((v)=>v.resource_id==='product-metrics').length,1);checkpoints.push('lost response is reconciled and produces exactly one service object');
 const ownership=await create('ds-library');await fault('delay_before',1500);const owned=await confirm(ownership);await api('/session','DELETE');await api('/demo/session','POST',{user_id:'maxim'});
 const foreign=await context.request.get(`${base}/api/attempts/${owned.attempt_id}`);assert.equal(foreign.status(),404);await pause(2200);await fault('normal');await api('/demo/session','POST',{user_id:'alina'});await poll(async()=>{const a=await api(`/attempts/${owned.attempt_id}`);return a.state==='succeeded';},'owner survives logout');checkpoints.push('confirmed work retains its original owner after logout and user switch');
 await fs.writeFile('docs/verification/resilience.json',JSON.stringify({date:'2026-11-14',mode:'fake',checkpoints},null,2));console.log(checkpoints.join('\n'));
}finally{try{await fault('normal');}catch{}await context.close();await browser.close();}
