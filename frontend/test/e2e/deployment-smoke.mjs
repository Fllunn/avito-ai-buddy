import { chromium } from 'playwright';
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';

const base = process.env.BUDDY_BASE_URL ?? 'https://semenovm.ru/avito_buddy';
const origin = new URL(base).origin;
const browser = await chromium.launch({ headless: true });
const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
const page = await context.newPage();
const errors = [];
const failedResources = [];
page.on('pageerror', error => errors.push(error.message));
page.on('response', response => {
  if (response.url().startsWith(base) && response.status() >= 400 && !response.url().endsWith('/api/me')) {
    failedResources.push(`${response.status()} ${response.url()}`);
  }
});
try {
  const anonymous = await context.request.get(`${base}/api/me`);
  assert.equal(anonymous.status(), 401);
  await page.goto(`${base}/`);
  await page.getByLabel('Демо-профиль').selectOption('alina');
  await page.getByRole('heading', { name: 'Привет, Алина!' }).waitFor();
  const cookies = await context.cookies();
  const session = cookies.find(cookie => cookie.name === 'buddy_session');
  assert(session?.secure && session.httpOnly);
  assert.equal(session.path, '/avito_buddy/');
  await page.getByRole('button', { name: /Мой онбординг/ }).first().click();
  await page.getByRole('dialog', { name: 'Мой онбординг', exact: true }).waitFor();
  await page.keyboard.press('Escape');
  await page.getByLabel('Сообщение AI-buddy').fill('Как оформить отгул?');
  await page.getByRole('button', { name: 'Отправить', exact: true }).click();
  const link = page.locator('.source-link').filter({ hasText: 'Как оформить отгул' }).last();
  await link.waitFor({ timeout: 20000 });
  assert((await link.getAttribute('href')).startsWith('/avito_buddy/demo/docs/'));
  const doc = await context.request.get(new URL(await link.getAttribute('href'), origin).href);
  assert.equal(doc.status(), 200);
  assert((await doc.text()).includes('Демонстрационная'));
  const history = await context.request.get(`${base}/api/chat`);
  const messages = (await history.json()).items;
  assert(messages.some(message => message.role === 'user' && message.text === 'Как оформить отгул?'));
  await page.reload();
  await page.getByRole('heading', { name: 'Привет, Алина!' }).waitFor();
  await page.getByRole('log').getByText('Как оформить отгул?', { exact: true }).last().waitFor();
  assert.deepEqual(errors, []);
  assert.deepEqual(failedResources, []);
  await fs.mkdir('output/deployment', { recursive: true });
  await page.screenshot({ path: 'output/deployment/smoke.png', fullPage: true });
  console.log(JSON.stringify({ base, status: 'passed', checks: ['TLS', 'anonymous-401', 'login', 'scoped-secure-cookie', 'onboarding', 'chat', 'document-link', 'reload'], messages: messages.length }));
} finally {
  await browser.close();
}
