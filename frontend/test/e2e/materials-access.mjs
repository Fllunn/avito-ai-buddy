import {chromium} from 'playwright';import assert from 'node:assert/strict';import fs from 'node:fs/promises';
const base='http://localhost:15173';const browser=await chromium.launch({headless:true});const context=await browser.newContext();const page=await context.newPage();
try{
 const r=await context.request.post(base+'/api/demo/session',{data:{user_id:'alina'},headers:{Origin:base}});assert(r.ok());
 await page.goto(base);await page.getByRole('heading',{name:'Привет, Алина!'}).waitFor();
 await page.getByLabel('Сообщение AI-buddy').fill('Как получить доступ?');await page.getByRole('button',{name:'Отправить',exact:true}).click();
 await page.locator('.source-link').filter({hasText:'Как запросить доступ'}).last().waitFor();
 assert.equal((await context.request.get(base+'/demo/docs/doc-access-howto')).status(),200);
 await page.getByRole('button',{name:'Нужен доступ',exact:true}).click();await page.getByRole('option',{name:'Проект PAY в трекере задач',exact:true}).waitFor({state:'attached'});
 await page.getByLabel('Ресурс').selectOption('tracker-pay');await page.getByRole('button',{name:'Подготовить карточку',exact:true}).click();
 const card=page.getByRole('article',{name:'Карточка подтверждения'});await card.getByText('Заявка на доступ',{exact:true}).waitFor();
 await card.getByRole('button',{name:'Подтвердить',exact:true}).click();
 let object;for(let i=0;i<150;i++){const r=await context.request.get(base+'/api/onboarding');assert(r.ok());object=(await r.json()).requests.find(r=>r.resource_id==='tracker-pay'&&r.status==='created');if(object)break;await new Promise(r=>setTimeout(r,100));}assert(object);
 await page.reload();await page.getByRole('heading',{name:'Привет, Алина!'}).waitFor();
 const chat=await (await context.request.get(base+'/api/chat')).json();assert.equal(chat.items.filter(m=>m.text.startsWith('Привет, Алина Соколова! Я AI-buddy')).length,1);
 await fs.writeFile('docs/verification/materials-access.json',JSON.stringify({date:'2026-11-14',mode:'fake',authorized_source:true,request_id:object.id,status:object.status,repeated_login_welcome_count:1},null,2));console.log('materials → access confirmation → persisted created; welcome not duplicated');
}finally{await context.close();await browser.close();}
