// Тесты-доказательства ревью изоляции (output/isolation-review), п.5: фронтенд.
// Кладутся во временную копию: frontend/src/isoreview_app.test.tsx.
// Сеть — через MSW; «сервер» хранит личную ленту каждого профиля.
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import { http, HttpResponse } from 'msw';
import { setupServer } from 'msw/node';
import { App } from './App';
import { useUI } from './stores/ui';
import { resetSession } from './api/client';

const now = '2026-11-12T07:00:00Z';
const ALINA = 'МАРКЕРАЛИНА77';
const MAXIM = 'МАРКЕРМАКСИМ42';
let user: string | null = null;
const feeds: Record<string, string[]> = {};
const profile = (id: string) => ({ id, name: id === 'maxim' ? 'Максим Орлов' : 'Алина Соколова', role: 'intern', team_id: id === 'maxim' ? 'search' : 'payments', start_date: '2026-11-09', buddy_id: 'igor', manager_id: 'olga', curator_id: 'daria', policy_version: 1 });
const step = (id: string) => ({ id: `${id}-P5`, intern_id: id, code: 'P5', title: `Шаг ${id}`, period: 1, owner_id: id, closure: 'manual', status: 'not_started', dependencies: [], source: 'Демо-план', updated_at: now, version: 1, blocked: false, overdue: false, own: true });
const chat = (id: string) => ({ items: (feeds[id] ?? []).map((text, i) => ({ id: i + 1, sequence: i + 1, client_message_id: null, role: 'assistant', kind: 'result', text, intent: '', slots: {}, form_required: false, created_at: now })), next_cursor: (feeds[id] ?? []).length, active_cards: [], llm_mode: 'fake' });
const unauthenticated = () => HttpResponse.json({ error: { code: 'UNAUTHENTICATED', fields: {}, retryable: false }, request_id: 'r' }, { status: 401 });
const server = setupServer(
  http.get('*/api/executions', () => HttpResponse.json({ items: [] })),
  http.get('*/api/me', () => user ? HttpResponse.json({ profile: profile(user), capabilities: { demo: true, service_actions: true } }) : unauthenticated()),
  http.post('*/api/demo/session', async ({ request }) => { const body = await request.json() as { user_id: string }; user = body.user_id; return HttpResponse.json({ profile: profile(user) }); }),
  http.delete('*/api/session', () => { user = null; return new HttpResponse(null, { status: 204 }); }),
  http.get('*/api/onboarding', () => user ? HttpResponse.json({ steps: [step(user)], next_steps: [step(user)], waiting_on_others: [], requests: [], week: 1, read_only: false, as_of: now }) : unauthenticated()),
  http.get('*/api/chat', () => user ? HttpResponse.json(chat(user)) : unauthenticated()),
  http.post('*/api/chat/messages', () => HttpResponse.json({ job_id: '00000000-0000-4000-8000-000000000001', message_id: 1 })),
  http.get('*/api/jobs/:id', () => HttpResponse.json({ id: 'job', state: 'completed', result: { items: [], mode: 'fake' } })),
);
beforeAll(() => { server.listen(); vi.stubEnv('VITE_API_BASE_URL', 'http://localhost'); });
afterAll(() => { server.close(); vi.unstubAllEnvs(); });
beforeEach(() => { user = null; feeds.alina = [`Привет, Алина! ${ALINA}`]; feeds.maxim = [`Привет, Максим! ${MAXIM}`]; resetSession(); useUI.getState().reset(); });
afterEach(() => server.resetHandlers());
const choose = (id: string) => fireEvent.change(screen.getByLabelText('Демо-профиль'), { target: { value: id } });
const settle = () => act(() => new Promise((resolve) => setTimeout(resolve, 150)));

describe('isolation: profile switch in one tab', () => {
  it('drops a late chat response of the previous session', async () => {
    let release!: () => void;
    const gate = new Promise<void>((resolve) => { release = resolve; });
    server.use(http.get('*/api/chat', async () => {
      const asked = user;
      if (asked === 'alina') { await gate; return HttpResponse.json(chat('alina')); }
      return HttpResponse.json(chat(asked!));
    }));
    render(<App />); await screen.findByText('Вход выполняется через выбор профиля сверху.');
    choose('alina'); await screen.findByRole('heading', { name: 'Привет, Алина!' });
    choose('maxim'); await screen.findByRole('heading', { name: 'Привет, Максим!' });
    await screen.findByText(new RegExp(MAXIM));
    release(); await settle();
    expect(screen.queryByText(new RegExp(ALINA))).not.toBeInTheDocument();
    expect(screen.queryByText('Шаг alina')).not.toBeInTheDocument();
  });

  it('does not show cached data of the previous profile while the new one loads', async () => {
    render(<App />); await screen.findByText('Вход выполняется через выбор профиля сверху.');
    choose('alina'); await screen.findByText(new RegExp(ALINA));
    fireEvent.change(screen.getByLabelText('Сообщение AI-buddy'), { target: { value: `черновик ${ALINA}` } });
    let release!: () => void;
    const gate = new Promise<void>((resolve) => { release = resolve; });
    server.use(http.get('*/api/chat', async () => { await gate; return HttpResponse.json(chat(user!)); }), http.get('*/api/onboarding', async () => { await gate; return HttpResponse.json({ steps: [step(user!)], next_steps: [], waiting_on_others: [], requests: [], week: 1, read_only: false, as_of: now }); }));
    choose('maxim'); await screen.findByRole('heading', { name: 'Привет, Максим!' });
    expect(screen.queryByText(new RegExp(ALINA))).not.toBeInTheDocument();
    expect(screen.queryByText('Шаг alina')).not.toBeInTheDocument();
    expect(screen.getByLabelText('Сообщение AI-buddy')).toHaveValue('');
    expect(useUI.getState().draft).toBe('');
    release(); await screen.findByText(new RegExp(MAXIM));
    expect(screen.queryByText(new RegExp(ALINA))).not.toBeInTheDocument();
  });

  it('logout and re-login restore only the own feed', async () => {
    render(<App />); await screen.findByText('Вход выполняется через выбор профиля сверху.');
    choose('alina'); await screen.findByText(new RegExp(ALINA));
    fireEvent.click(screen.getByText('Выйти')); await screen.findByRole('heading', { name: 'Привет! С чего начнём?' });
    expect(screen.queryByText(new RegExp(ALINA))).not.toBeInTheDocument();
    choose('maxim'); await screen.findByText(new RegExp(MAXIM));
    expect(screen.queryByText(new RegExp(ALINA))).not.toBeInTheDocument();
    choose('alina'); await screen.findByText(new RegExp(ALINA));
    expect(screen.queryByText(new RegExp(MAXIM))).not.toBeInTheDocument();
  });
});

// IR-04 (P3, демонстрация): вторая вкладка того же браузера. Cookie общий, поэтому
// после входа Максима в другой вкладке эта вкладка продолжает показывать «Привет,
// Алина!», а следующая перезагрузка ленты приносит данные Максима. Клиент не сверяет
// владельца ответа с профилем сессии. Тест ПАДАЕТ на текущем коде.
describe('isolation: second tab of the same browser (P3 demo)', () => {
  it('does not render another profile data under the current profile header', async () => {
    render(<App />); await screen.findByText('Вход выполняется через выбор профиля сверху.');
    choose('alina'); await screen.findByText(new RegExp(ALINA));
    // Мок повторяет правило бэкенда после исправления: профиль вкладки ≠ сессии → 409.
    const tabGuard = (request: Request) => { const tab = request.headers.get('X-Buddy-Profile'); return tab && tab !== user ? HttpResponse.json({ error: { code: 'PROFILE_CHANGED', fields: {}, retryable: false }, request_id: 'r' }, { status: 409 }) : null; };
    server.use(
      http.get('*/api/chat', ({ request }) => tabGuard(request) ?? HttpResponse.json(chat(user!))),
      http.get('*/api/onboarding', ({ request }) => tabGuard(request) ?? HttpResponse.json({ steps: [step(user!)], next_steps: [step(user!)], waiting_on_others: [], requests: [], week: 1, read_only: false, as_of: now })),
      http.get('*/api/me', ({ request }) => tabGuard(request) ?? HttpResponse.json({ profile: profile(user!), capabilities: { demo: true, service_actions: true } })),
      http.post('*/api/chat/messages', ({ request }) => tabGuard(request) ?? HttpResponse.json({ job_id: '00000000-0000-4000-8000-000000000001', message_id: 1 })),
    );
    user = 'maxim'; // вход Максима в другой вкладке перезаписал общий cookie
    fireEvent.change(screen.getByLabelText('Сообщение AI-buddy'), { target: { value: 'Что дальше?' } });
    fireEvent.click(screen.getByLabelText('Отправить'));
    await settle(); await settle();
    const mixed = !!screen.queryByRole('heading', { name: 'Привет, Алина!' }) && !!screen.queryByText(new RegExp(MAXIM));
    await waitFor(() => expect(mixed).toBe(false));
  });
});
