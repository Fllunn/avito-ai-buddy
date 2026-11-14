import {chromium} from 'playwright';import fs from 'node:fs/promises';import assert from 'node:assert/strict';
const browser=await chromium.launch({headless:true});const context=await browser.newContext({viewport:{width:390,height:844}});const page=await context.newPage();
try{
 await page.goto('http://localhost:15173');await page.getByLabel('Демо-профиль').selectOption('alina');await page.getByRole('heading',{name:'Привет, Алина!'}).waitFor();
 assert(await page.getByLabel('Сообщение AI-buddy').isEnabled());
 const dimensions=await page.evaluate(()=>({scroll:document.documentElement.scrollWidth,width:innerWidth}));assert(dimensions.scroll<=dimensions.width+1,JSON.stringify(dimensions));
 await page.screenshot({path:'docs/verification/screenshots/mobile.png',fullPage:true});
 await page.setViewportSize({width:1440,height:1000});await page.reload();await page.getByRole('heading',{name:'Привет, Алина!'}).waitFor();await page.screenshot({path:'docs/verification/screenshots/desktop.png'});
 await fs.writeFile('docs/verification/layout.json',JSON.stringify({date:'2026-11-14',mobile:dimensions,composer_enabled:true},null,2));console.log('mobile layout and desktop screenshot passed');
}finally{await context.close();await browser.close();}
