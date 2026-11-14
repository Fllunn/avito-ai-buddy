import {chromium} from 'playwright';
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
const base=process.env.BUDDY_BASE_URL??'http://localhost:25173';
assert(['localhost','127.0.0.1'].includes(new URL(base).hostname));
const root='docs/verification/redesign';
const layoutDate=process.env.BUDDY_LAYOUT_DATE??'2026-12-03';
const browser=await chromium.launch({headless:true});
const page=await browser.newPage({viewport:{width:1440,height:900}});
const errors=[];page.on('pageerror',e=>errors.push(e.message));
const results=[];
try{
 await page.goto(base);await page.getByLabel('Демо-профиль').selectOption('alina');await page.getByRole('heading',{name:'Привет, Алина!'}).waitFor();
 await page.locator('.main-step h2').filter({hasNotText:'Загружаем'}).waitFor();
 for(const [width,height] of [[1440,900],[1024,768],[390,844],[320,740]]){
  await page.setViewportSize({width,height});await page.waitForTimeout(250);await page.screenshot({path:`${root}/screenshots/workspace-${width}.png`,fullPage:true});
  assert(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),`overflow at ${width}`);
  const input=page.getByLabel('Сообщение AI-buddy');await input.fill('Черновик остаётся со мной');
  const trigger=width<1024?page.locator('.mobile-plan'):page.getByRole('button',{name:/Мой онбординг/}).first();
  await trigger.focus();await page.keyboard.press('Enter');const dialog=page.getByRole('dialog',{name:'Мой онбординг',exact:true});await dialog.waitFor();
  assert(await dialog.evaluate(d=>d.contains(document.activeElement)),'focus inside plan');
  await page.keyboard.press('Tab');assert(await dialog.evaluate(d=>d.contains(document.activeElement)),'tab inside plan');
  await page.screenshot({path:`${root}/screenshots/plan-${width}.png`,fullPage:true});
  await page.keyboard.press('Escape');await dialog.waitFor({state:'hidden'});assert(await trigger.evaluate(e=>e===document.activeElement),'restored focus');assert.equal(await input.inputValue(),'Черновик остаётся со мной');
  results.push({width,height,overflow:false,draft_preserved:true,keyboard_dialog:true});
 }
 await page.setViewportSize({width:390,height:450});const composer=page.getByLabel('Сообщение AI-buddy');await composer.fill('Проверка короткого экрана');await composer.scrollIntoViewIfNeeded();await composer.focus();const box=await composer.boundingBox();assert(box&&box.y>=0&&box.y+box.height<=450,'composer with keyboard-sized viewport');
 await page.emulateMedia({reducedMotion:'reduce'});await page.getByRole('button',{name:'Нужен отгул',exact:true}).click();
 await page.getByLabel('Первый день',{exact:true}).fill(layoutDate);await page.getByLabel('Последний день',{exact:true}).fill(layoutDate);
 await page.getByRole('button',{name:'Подготовить карточку',exact:true}).click();await page.getByRole('article',{name:'Карточка подтверждения'}).waitFor();
 await page.getByRole('status').filter({hasText:'Ждём тебя'}).waitFor();
 assert.equal(await page.locator('.buddy').first().evaluate(e=>getComputedStyle(e).animationName),'none');assert.equal(await page.locator('.execution-map [role=status]').evaluate(e=>e.closest('[role=log]')!==null),false,'route is not nested in another live region');assert(await page.locator('.buddy').evaluateAll(nodes=>nodes.every(n=>n.getAttribute('aria-hidden')==='true')),'decorative mascots are silent');
 await page.screenshot({path:`${root}/screenshots/confirmation-reduced-motion.png`,fullPage:true});
 await page.getByRole('button',{name:'Показать маршрут'}).click();await page.getByRole('dialog',{name:'Маршрут запроса'}).waitFor();await page.screenshot({path:`${root}/screenshots/route-mobile.png`,fullPage:true});await page.keyboard.press('Escape');
 await page.setViewportSize({width:1440,height:900});await page.screenshot({path:`${root}/screenshots/confirmation-desktop.png`,fullPage:true});
 await page.evaluate(()=>{const sizes=[...document.querySelectorAll('*')].map(e=>({e,font:parseFloat(getComputedStyle(e).fontSize),line:parseFloat(getComputedStyle(e).lineHeight)}));for(const {e,font,line} of sizes){if(e instanceof HTMLElement){e.style.setProperty('font-size',`${font*2}px`,'important');if(Number.isFinite(line))e.style.setProperty('line-height',`${line*2}px`,'important');}}});await page.setViewportSize({width:320,height:740});
 assert(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),'overflow with larger text');await page.screenshot({path:`${root}/screenshots/text-200.png`,fullPage:true});
 assert.deepEqual(errors,[]);await fs.writeFile(`${root}/layout.json`,JSON.stringify({results,reduced_motion:true,text_200:true,page_errors:errors,limitations:['Экранная клавиатура реального телефона и ручной скринридер не проверены.']},null,2));
}finally{await browser.close();}
