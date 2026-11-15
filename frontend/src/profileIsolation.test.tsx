// Regression tests for IR-04 (output/isolation-review): a tab renders only the
// profile of the current session, even when another tab rotated the cookie.
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
let posted: string[] = [];
let headers: (string | null)[] = [];
const profile = (id: string) => ({ id, name: id === 'maxim' ? 'Максим Орлов' : 'Алина Соколова', role: 'intern', team_id: id === 'maxim' ? 'search' : 'payments', start_date: '2026-11-09', buddy_id: 'igor', manager_id: 'olga', curator_id: 'daria', policy_version: 1 });
const step = (id: string) => ({ id: `${id}-P5`, intern_id: id, code: 'P5', title: `Шаг ${id}`, period: 1, owner_id: id, closure: 'manual', status: 'not_started', dependencies: [], source: 'Демо-план', updated_at: now, version: 1, blocked: false, overdue: false, own: true });
const feed = (id: string) => ({ items: [{ id: 1, sequence: 1, client_message_id: null, role: 'assistant', kind: 'result', text: id === 'maxim' ? MAXIM : ALINA, intent: '', slots: {}, form_required: false, created_at: now }], next_cursor: 1, active_cards: [], llm_mode: 'fake' });
const problem = (code: string, status: number) => HttpResponse.json({ error: { code, fields: {}, retryable: false }, request_id: 'r' }, { status });
// Emulates the backend rule: a tab profile that differs from the session is refused.
const guard = (request: Request) => {
  const tab = request.headers.get('X-Buddy-Profile'); headers.push(tab);
  if (!user) return problem('UNAUTHENTICATED', 401);
  if (tab && tab !== user) return problem('PROFILE_CHANGED', 409);
  return null;
};
const server = setupServer(
  http.get('*/api/executions', ({ request }) => guard(request) ?? HttpResponse.json({ items: [] })),
  http.get('*/api/me', ({ request }) => guard(request) ?? HttpResponse.json({ profile: profile(user!), capabilities: { demo: true, service_actions: true } })),
  http.post('*/api/demo/session', async ({ request }) => { const body = await request.json() as { user_id: string }; user = body.user_id; return HttpResponse.json({ profile: profile(user) }); }),
  http.delete('*/api/session', () => { user = null; return new HttpResponse(null, { status: 204 }); }),
  http.get('*/api/onboarding', ({ request }) => guard(request) ?? HttpResponse.json({ steps: [step(user!)], next_steps: [step(user!)], waiting_on_others: [], requests: [], week: 1, read_only: false, as_of: now })),
  http.get('*/api/chat', ({ request }) => guard(request) ?? HttpResponse.json(feed(user!))),
  http.post('*/api/chat/messages', async ({ request }) => { const refused = guard(request); if (refused) return refused; const body = await request.json() as { text: string }; posted.push(`${user}:${body.text}`); return HttpResponse.json({ job_id: '00000000-0000-4000-8000-000000000001', message_id: 2 }); }),
  http.get('*/api/jobs/:id', ({ request }) => guard(request) ?? HttpResponse.json({ id: 'job', state: 'completed', result: { items: [], mode: 'fake' } })),
);
beforeAll(() => { server.listen(); vi.stubEnv('VITE_API_BASE_URL', 'http://localhost'); });
afterAll(() => { server.close(); vi.unstubAllEnvs(); });
beforeEach(() => { user = null; posted = []; headers = []; resetSession(); useUI.getState().reset(); });
afterEach(() => server.resetHandlers());
const choose = (id: string) => fireEvent.change(screen.getByLabelText('Демо-профиль'), { target: { value: id } });
const settle = () => act(() => new Promise((resolve) => setTimeout(resolve, 150)));
async function asAlina() { render(<App />); await screen.findByText('Вход выполняется через выбор профиля сверху.'); choose('alina'); await screen.findByText(ALINA); }
function send(text: string) { fireEvent.change(screen.getByLabelText('Сообщение AI-buddy'), { target: { value: text } }); fireEvent.click(screen.getByLabelText('Отправить')); }

describe('tab profile isolation', () => {
  it('sends the rendered profile with requests', async () => {
    await asAlina(); await settle();
    expect(headers).toContain('alina');
  });

  it('refuses to render another profile under the current header and does not resend', async () => {
    await asAlina();
    user = 'maxim'; // another tab rotated the shared cookie
    send(`черновик ${ALINA}`);
    await screen.findByRole('heading', { name: 'Привет, Максим!' });
    expect(screen.getByText(/Профиль сменён в другой вкладке/)).toBeInTheDocument();
    expect(screen.queryByText(ALINA)).not.toBeInTheDocument();
    expect(screen.queryByRole('heading', { name: 'Привет, Алина!' })).not.toBeInTheDocument();
    await screen.findByText(MAXIM); await settle();
    expect(posted).toEqual([]);
    expect(screen.getByLabelText('Сообщение AI-buddy')).toHaveValue('');
  });

  it('shows profile selection when rereading the profile fails', async () => {
    await asAlina();
    user = 'maxim';
    server.use(http.get('*/api/me', ({ request }) => guard(request) ?? new HttpResponse(null, { status: 503 })));
    send('Что дальше?');
    await screen.findByText(/Профиль сменён в другой вкладке/);
    expect(screen.queryByText(ALINA)).not.toBeInTheDocument();
    expect(screen.getByLabelText('Демо-профиль')).not.toBeDisabled();
  });

  it('a switch in the same tab is not reported as another tab', async () => {
    await asAlina();
    choose('maxim'); await screen.findByText(MAXIM); await settle();
    expect(screen.queryByText(/Профиль сменён в другой вкладке/)).not.toBeInTheDocument();
    await waitFor(() => expect(headers.filter((h) => h === 'alina').length).toBeGreaterThan(0));
    expect(headers.slice(-3)).not.toContain('alina');
  });
});
