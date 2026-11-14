import { chromium } from 'playwright';
import fs from 'node:fs/promises';
import assert from 'node:assert/strict';
import { performance } from 'node:perf_hooks';
const base = process.env.BUDDY_BASE_URL ?? 'http://localhost:15173';
const panel = process.env.BUDDY_PANEL_URL ?? 'http://localhost:18081';
for (const raw of [base, panel]) assert(['localhost', '127.0.0.1'].includes(new URL(raw).hostname), 'Acceptance runs only on a local synthetic stand');
const envFile = process.env.BUDDY_ENV_FILE ?? '/private/tmp/buddy-acceptance.env';
const env = Object.fromEntries((await fs.readFile(envFile, 'utf8')).split('\n').filter((line) => line && !line.startsWith('#')).map((line) => { const i = line.indexOf('='); return [line.slice(0, i), line.slice(i + 1)]; }));
const browser = await chromium.launch({ headless: true });
const context = await browser.newContext({ viewport: { width: 1440, height: 1000 }, ...(process.env.BUDDY_RECORD === '1' ? { recordVideo: { dir: 'output/demo', size: { width: 1440, height: 1000 } } } : {}) });
const page = await context.newPage();
const errors = [];
page.on('pageerror', (e) => errors.push(e.message));
const wait = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
const checkpoints = [];
const checkpoint = (name) => { checkpoints.push(name); console.log(name); };
async function api(path, method = 'GET', data) {
  const result = await context.request.fetch(`${base}/api${path}`, { method, data, headers: { Origin: base } });
  const raw = result.status() === 204 ? null : await result.json();
  assert(result.ok(), `${method} ${path}: ${result.status()} ${JSON.stringify(raw)}`); return raw;
}
async function presenter(path, data) {
  const result = await context.request.fetch(`${panel}/demo${path}`, { method: path === '/session' ? 'POST' : 'PATCH', data, headers: { Origin: panel } });
  assert(result.ok(), `Presenter ${path}: ${result.status()}`); return result.json();
}
async function poll(check, label, timeout = 15000) { const start = Date.now(); while (Date.now() - start < timeout) { const value = await check(); if (value) return value; await wait(100); } throw new Error(`Timed out: ${label}`); }
async function refresh() { await page.getByRole('button', { name: 'Проверить статусы' }).click(); await wait(1400); }
async function card() { return page.getByRole('article', { name: 'Карточка подтверждения' }); }
async function confirmCard() { const target = await card(); await target.getByRole('button', { name: 'Подтвердить', exact: true }).click(); await poll(async () => (await api('/chat')).active_cards.every((c) => !c.attempt || !['queued', 'sending'].includes(c.attempt.state)), 'service result'); await wait(1400); }
try {
  await page.goto(base);
  await page.getByLabel('Демо-профиль').selectOption('alina');
  await page.getByRole('heading', { name: 'Привет, Алина!' }).waitFor();
  await page.getByRole('button', { name: 'С чего начать?', exact: true }).click();
  await page.getByRole('log').getByText(/Ближайший шаг:/).first().waitFor();
  checkpoint('orientation: profile and next step');
  await page.getByRole('button', { name: 'Нужен доступ', exact: true }).click();
  await page.getByRole('option', { name: 'Пространство команды «Платежи»', exact: true }).waitFor({ state: 'attached' });
  await page.getByLabel('Ресурс').selectOption('space-payments');
  await page.getByRole('button', { name: 'Подготовить карточку', exact: true }).click();
  await page.getByText('Заявка на доступ', { exact: true }).waitFor();
  const first = (await api('/chat')).active_cards[0];
  assert.equal(first.parameters.resource_id, 'space-payments');
  const second = await context.newPage(); await second.goto(base); await second.getByText('Заявка на доступ', { exact: true }).waitFor();
  await (await card()).getByRole('button', { name: 'Изменить', exact: true }).click();
  await page.getByLabel('Уровень').selectOption('edit');
  await page.getByRole('button', { name: 'Сохранить новую версию', exact: true }).click();
  await page.getByText('Демо · версия 2', { exact: true }).waitFor();
  const stale = await context.request.post(`${base}/api/cards/${first.id}/confirm`, { data: { expected_version: 1 }, headers: { Origin: base } });
  assert.equal(stale.status(), 409);
  await second.close();
  await confirmCard();
  let plan = await api('/onboarding'); let access = plan.requests.find((r) => r.service === 'access' && r.resource_id === 'space-payments');
  assert(access && access.status === 'created');
  await presenter('/session', { secret: env.DEMO_ADMIN_SECRET });
  await presenter(`/access-requests/${access.id}/status`, { expected_version: access.version, status: 'approved' });
  await refresh();
  plan = await poll(async () => { const p = await api('/onboarding'); return p.requests.some((r) => r.id === access.id && r.status === 'approved') ? p : null; }, 'approval projection');
  assert.equal(plan.steps.find((s) => s.code === 'P6').status, 'done'); assert.equal(plan.steps.find((s) => s.code === 'P7').blocked, false);
  await page.screenshot({ path: 'docs/verification/screenshots/onboarding-approved.png', fullPage: true });
  checkpoint('access: edit, stale version, confirmation, approved and P7');
  await page.getByLabel('Сообщение AI-buddy').fill('Как оформить отгул?'); await page.getByRole('button', { name: 'Отправить', exact: true }).click();
  await page.locator('.source-link').filter({ hasText: 'Как оформить отгул' }).waitFor();
  const doc = await context.request.get(`${base}/demo/docs/doc-timeoff-howto`); assert.equal(doc.status(), 200);
  checkpoint('documents: grounded answer and working source');
  await page.getByRole('button', { name: 'Нужен отгул', exact: true }).click();
  await page.getByLabel('Первый день', { exact: true }).fill('2026-11-13'); await page.getByLabel('Последний день', { exact: true }).fill('2026-11-16');
  await page.getByRole('button', { name: 'Подготовить карточку', exact: true }).click(); await page.getByText('Запрос отгула', { exact: true }).waitFor();
  await confirmCard(); plan = await api('/onboarding'); let absence = plan.requests.find((r) => r.service === 'time_off' && r.start_date === '2026-11-13'); assert(absence && absence.status === 'submitted' && absence.workdays === 2);
  await presenter(`/time-off-requests/${absence.id}/status`, { expected_version: absence.version, status: 'approved' }); await refresh();
  const oldArticle = page.locator('.plan-step').filter({ hasText: absence.id }); await oldArticle.getByRole('button', { name: 'Перенести', exact: true }).click();
  await page.getByLabel('Первый день', { exact: true }).fill('2026-11-20'); await page.getByLabel('Последний день', { exact: true }).fill('2026-11-20');
  await page.getByRole('button', { name: 'Подготовить карточку', exact: true }).click(); await page.getByText('Запрос отгула', { exact: true }).waitFor(); await confirmCard();
  await page.getByRole('log').getByText(/Предыдущее действие завершено. Перейдём/).last().waitFor();
  await page.getByLabel('Сообщение AI-buddy').fill('да'); await page.getByRole('button', { name: 'Отправить', exact: true }).click();
  await page.getByText('Подтвердить отмену', { exact: true }).waitFor(); await confirmCard();
  plan = await api('/onboarding'); assert.equal(plan.requests.find((r) => r.id === absence.id).status, 'cancelled'); assert(plan.requests.some((r) => r.service === 'time_off' && r.start_date === '2026-11-20' && r.status === 'submitted'));
  await page.reload(); await page.getByRole('heading', { name: 'Привет, Алина!' }).waitFor(); await page.getByText(`${access.id} · Согласован`, { exact: true }).waitFor();
  checkpoint('absence: submitted, approval, transfer with two confirmations, cancellation and reload');
  // Measure local operations after warmup; the card remains a draft and is discarded afterwards.
  const draft = await api('/cards', 'POST', { type: 'time_off_create', parameters: { start_date: '2026-11-23', end_date: '2026-11-23' } });
  const measurements = {};
  for (const [name, path] of [['plan', '/onboarding'], ['history', '/chat'], ['card', `/cards/${draft.card.id}`]]) {
    for (let i = 0; i < 5; i++) await api(path);
    const values = [];
    for (let i = 0; i < 100; i++) { const start = performance.now(); await api(path); values.push(performance.now() - start); }
    values.sort((a, b) => a - b); const p95 = values[94]; assert(p95 < 1000, `${name} p95 ${p95}`); measurements[name] = { requests: 100, warmup: 5, p95_ms: Number(p95.toFixed(2)), max_ms: Number(values[99].toFixed(2)) };
  }
  await api(`/cards/${draft.card.id}/discard`, 'POST', { expected_version: draft.card.version });
  await fs.writeFile('docs/verification/performance.json', JSON.stringify({ date: '2026-11-14', timezone: 'Asia/Yekaterinburg', stand: 'isolated Docker Compose, fake LLM, 100 sequential HTTP requests after 5 warmups, 1440x1000 Chromium', measurements }, null, 2));
  checkpoint('performance: 100 requests per local operation, p95 below 1 second');
  await page.getByLabel('Демо-профиль').selectOption('maxim'); await page.getByRole('heading', { name: 'Привет, Максим!' }).waitFor();
  const foreign = await context.request.get(`${base}/api/cards/${first.id}`); assert.equal(foreign.status(), 404); const maxPlan = await api('/onboarding'); assert(maxPlan.steps.every((s) => s.intern_id === 'maxim'));
  for (const id of ['igor', 'olga', 'daria']) { await page.getByLabel('Демо-профиль').selectOption(id); await poll(async () => { const response = await context.request.get(`${base}/api/me`); if (response.status()===401) return false; assert(response.ok()); const me=await response.json(); return me.profile.id===id; }, `role ${id}`); const me = await api('/me'); assert.equal(me.capabilities.service_actions, false); const own = await api('/onboarding'); assert(own.steps.every((s) => s.owner_id === id)); }
  checkpoint('permissions: two teams, all four roles and foreign object denial');
  assert.deepEqual(errors, []);
  await fs.writeFile('docs/verification/acceptance.json', JSON.stringify({ date: '2026-11-14', mode: 'fake', checkpoints, page_errors: errors, access_id: access.id, absence_id: absence.id }, null, 2));
} catch (error) {
  await page.screenshot({path:'docs/verification/screenshots/acceptance-failure.png',fullPage:true});
  throw error;
} finally {
  await context.close(); await browser.close();
}
