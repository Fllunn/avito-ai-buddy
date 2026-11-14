import {request} from 'playwright';import fs from 'node:fs/promises';import assert from 'node:assert/strict';import {execFileSync} from 'node:child_process';
const base='http://localhost:15173';const project='ai-buddy-acceptance-20261114';const envFile=process.env.BUDDY_ENV_FILE??'/private/tmp/buddy-acceptance.env';
const ctx=await request.newContext();
const get=async(path)=>{const r=await ctx.get(base+'/api'+path);assert(r.ok());return r.json();};
const compose=(...args)=>execFileSync('docker',['compose','-p',project,'--env-file',envFile,'-f','compose.yaml','-f','infra/acceptance.compose.yaml',...args],{stdio:'pipe'});
try{
 const r=await ctx.post(base+'/api/demo/session',{data:{user_id:'alina'},headers:{Origin:base}});assert(r.ok());
 const before=await get('/onboarding');const history=await get('/chat');
 compose('stop','frontend','api','mocks');compose('up','-d','--wait','--wait-timeout','60');
 const after=await get('/onboarding');const restored=await get('/chat');
 const objects=(v)=>v.requests.map(o=>[o.id,o.status,o.version]).sort();
 assert.deepEqual(objects(after),objects(before));for(const message of history.items) {assert(restored.items.some(item=>item.id===message.id && item.text===message.text));}
 await fs.writeFile('docs/verification/restart.json',JSON.stringify({date:'2026-11-14',mode:'fake',project,preserved_objects:objects(after),session_restored:true,history_preserved:true},null,2));console.log('restart: session, history and service object versions preserved');
}finally{await ctx.dispose();}
