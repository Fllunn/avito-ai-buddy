import {chromium} from 'playwright';import assert from 'node:assert/strict';import fs from 'node:fs/promises';
const base='http://localhost:15173';const browser=await chromium.launch({headless:true});const context=await browser.newContext();const page=await context.newPage();
try{
 const login=await context.request.post(base+'/api/demo/session',{data:{user_id:'alina'},headers:{Origin:base}});assert(login.ok());
 const get=async()=>{const r=await context.request.get(base+'/api/onboarding');assert(r.ok());return r.json();};
 const before=await get();const target=before.requests.find(r=>r.service==='access'&&r.resource_id==='monitoring'&&r.status==='created');assert(target);
 await page.goto(base);await page.getByRole('heading',{name:'Привет, Алина!'}).waitFor();
 await page.locator('.plan-step').filter({hasText:target.id}).getByRole('button',{name:'Подготовить отмену'}).click();
 const card=page.getByRole('article',{name:'Карточка подтверждения'});await card.getByText('Подтвердить отмену',{exact:true}).waitFor();
 assert.equal((await get()).requests.find(r=>r.id===target.id).status,'created');
 await card.getByRole('button',{name:'Подтвердить',exact:true}).click();
 let done=false;for(let i=0;i<150;i++){const p=await get();if(p.requests.some(r=>r.id===target.id&&r.status==='cancelled')){done=true;break;}await new Promise(r=>setTimeout(r,100));}assert(done);
 await page.reload();await page.locator('.plan-step').filter({hasText:target.id}).getByText(/Отменён/).waitFor();
 await fs.writeFile('docs/verification/access-cancel.json',JSON.stringify({date:'2026-11-14',mode:'fake',request_id:target.id,not_cancelled_before_confirmation:true,status:'cancelled',reload_preserved:true},null,2));console.log('access cancellation: explicit confirmation and persisted result passed');
}finally{await context.close();await browser.close();}
