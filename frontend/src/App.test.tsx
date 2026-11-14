import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import { http, HttpResponse } from 'msw';
import { setupServer } from 'msw/node';
import { App } from './App';
import { useUI } from './stores/ui';
import { resetSession, type Card } from './api/client';
const now = '2026-11-12T07:00:00Z';
let user: string | null = null;
let cards: Card[] = [];
let messages: { id: number; sequence: number; client_message_id: null; role: string; kind: string; text: string; intent: string; slots: Record<string, string>; form_required: boolean; created_at: string }[] = [];
let done = false;
const profile = (id: string) => ({ id, name: id === 'maxim' ? 'Максим Орлов' : 'Алина Соколова', role: 'intern', team_id: id === 'maxim' ? 'search' : 'payments', start_date: '2026-11-09', buddy_id: 'igor', manager_id: 'olga', curator_id: 'daria', policy_version: 1 });
const step = () => ({ id: `${user}-P5`, intern_id: user, code: 'P5', title: 'Завести конспект', period: 1, owner_id: user, closure: 'manual', status: done ? 'done' : 'not_started', dependencies: [], source: 'Демо-план', updated_at: now, version: 1, blocked: false, overdue: false, own: true });
const resource = { id: 'space-payments', name: 'Пространство Платежей', kind: 'recommended', levels: ['read', 'edit'], default_level: 'read', approver_id: 'olga', recommended_teams: ['payments'], synonyms: ['PAY'] };
const server = setupServer(
  http.get('*/api/executions', () => HttpResponse.json({items:[]})),
  http.get('*/api/me', () => user ? HttpResponse.json({ profile: profile(user), capabilities: { demo: true, service_actions: true } }) : HttpResponse.json({ error: { code: 'UNAUTHENTICATED', fields: {}, retryable: false }, request_id: 'request' }, { status: 401 })),
  http.post('*/api/demo/session', async ({ request }) => { const body = await request.json() as { user_id: string }; user = body.user_id; cards = []; messages = []; return HttpResponse.json({ profile: profile(user) }); }),
  http.delete('*/api/session', () => { user = null; return new HttpResponse(null, { status: 204 }); }),
  http.get('*/api/onboarding', () => HttpResponse.json({ steps: [step()], next_steps: done ? [] : [step()], waiting_on_others: [], requests: [], week: 1, read_only: false, as_of: now })),
  http.get('*/api/chat', () => HttpResponse.json({ items: messages, next_cursor: messages.length, active_cards: cards, llm_mode: 'fake' })),
  http.post('*/api/requests/refresh', () => HttpResponse.json({ job_id: 'refresh' })),
  http.get('*/api/jobs/:id', ({params}) => HttpResponse.json({ id: 'job', state: 'completed', result: { items: params.id === 'dialogue' ? [{ resources: [resource] }] : [], mode: 'fake' } })),
  http.post('*/api/dialogue/actions', () => HttpResponse.json({ job_id: 'dialogue', message_ids: [1] })),
  http.post('*/api/chat/messages', async ({ request }) => { const body = await request.json() as { text: string }; messages.push({ id: messages.length + 1, sequence: messages.length + 1, client_message_id: null, role: 'user', kind: 'text', text: body.text, intent: '', slots: {}, form_required: false, created_at: now }); return HttpResponse.json({ job_id: 'message', message_id: messages.length }); }),
  http.post('*/api/cards', async ({ request }) => { const body = await request.json() as Pick<Card, 'type' | 'parameters'>; const card: Card = { ...body, id: 'card', user_id: user!, version: 1, state: 'draft', expires_at: '2026-11-13T07:00:00Z', updated_at: now }; cards = [card]; return HttpResponse.json({ card }); }),
  http.patch('*/api/cards/:id', async ({ request }) => { const body = await request.json() as { changes: Card['parameters'] }; const card = cards[0]!; cards = [{ ...card, parameters: { ...card.parameters, ...body.changes }, version: card.version + 1 }]; return HttpResponse.json({ card: cards[0] }); }),
  http.post('*/api/cards/:id/confirm', () => { cards = []; return HttpResponse.json({ attempt_id: 'attempt', job_id: 'confirmed', state: 'queued' }); }),
  http.post('*/api/cards/:id/discard', () => { const card = { ...cards[0]!, state: 'discarded' }; cards = []; return HttpResponse.json({ card }); }),
  http.post('*/api/steps/:id/confirmation', () => HttpResponse.json({ confirmation_id: 'step-confirmation', summary: 'Отметить шаг выполненным?', expires_at: '2026-11-12T07:05:00Z' })),
  http.post('*/api/steps/:id/confirm', () => { done = !done; const { blocked: _blocked, overdue: _overdue, own: _own, ...data } = step(); return HttpResponse.json({ step: data }); }),
);
beforeAll(() => { server.listen({ onUnhandledFrame: 'error' }); vi.stubEnv('VITE_API_BASE_URL', 'http://localhost'); });
afterAll(() => { server.close(); vi.unstubAllEnvs(); });
beforeEach(() => { user = null; cards = []; messages = []; done = false; resetSession(); useUI.getState().reset(); });
afterEach(() => server.resetHandlers());
async function login() { render(<App />); await screen.findByText('Вход выполняется через выбор профиля сверху.'); fireEvent.change(screen.getByLabelText('Демо-профиль'), { target: { value: 'alina' } }); await screen.findByRole('heading', { name: 'Привет, Алина!' }); await screen.findAllByText('Завести конспект'); }

describe('Connected workspace', () => {
  it('shows login, authorized plan and first message then clears state on identity switch', async () => {
    await login(); expect(screen.getByText('Детерминированный fake')).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText('Сообщение AI-buddy'), { target: { value: 'Мой черновик' } });
    fireEvent.change(screen.getByLabelText('Демо-профиль'), { target: { value: 'maxim' } });
    await screen.findByRole('heading', { name: 'Привет, Максим!' }); expect(screen.getByLabelText('Сообщение AI-buddy')).toHaveValue('');
    fireEvent.click(screen.getByText('Выйти')); await screen.findByRole('heading', { name: 'Привет! С чего начнём?' });
  });
  it('sends text without blocking the page, prepares an absence and confirms it', async () => {
    await login(); fireEvent.change(screen.getByLabelText('Сообщение AI-buddy'), { target: { value: 'С чего начать?' } }); fireEvent.click(screen.getByLabelText('Отправить'));
    await screen.findByText('С чего начать?', { selector: 'p' }); expect(screen.getByLabelText('Сообщение AI-buddy')).not.toBeDisabled();
    fireEvent.click(screen.getByText('Нужен отгул')); const form = screen.getByRole('form', { name: 'Форма отгула' });
    fireEvent.change(within(form).getByLabelText('Первый день'), { target: { value: '2026-11-13' } }); fireEvent.change(within(form).getByLabelText('Последний день'), { target: { value: '2026-11-16' } }); fireEvent.click(within(form).getByText('Подготовить карточку'));
    await screen.findByRole('article', { name: 'Карточка подтверждения' }); fireEvent.click(screen.getByText('Изменить')); fireEvent.change(screen.getByLabelText('Последний день'), { target: { value: '2026-11-17' } }); fireEvent.click(screen.getByText('Сохранить новую версию'));
    await screen.findByText('Демо · версия 2'); fireEvent.click(screen.getByText('Отправить запрос на отгул')); await waitFor(() => expect(screen.queryByText('Отправить запрос на отгул')).not.toBeInTheDocument());
  });
  it('loads permitted resources, edits access parameters and discards without sending', async () => {
    await login(); fireEvent.click(screen.getByText('Нужен доступ')); await screen.findByRole('option', { name: 'Пространство Платежей' }, { timeout: 4000 });
    fireEvent.change(screen.getByLabelText('Ресурс'), { target: { value: 'space-payments' } }); fireEvent.change(screen.getByLabelText('Уровень'), { target: { value: 'edit' } }); fireEvent.click(screen.getByText('Подготовить карточку'));
    await screen.findByText('Заявка на доступ'); fireEvent.click(screen.getByText('Изменить')); fireEvent.change(screen.getByLabelText('Обоснование'), { target: { value: 'Для тестовой рабочей задачи' } }); fireEvent.click(screen.getByText('Сохранить новую версию'));
    await screen.findByText('Демо · версия 2'); fireEvent.click(screen.getByText('Не отправлять')); await waitFor(() => expect(screen.queryByText('Не отправлять')).not.toBeInTheDocument());
  });
  it('requires an explicit confirmation for a manual step and supports returning it to work', async () => {
    await login(); fireEvent.click(screen.getAllByRole('button',{name:/Мой онбординг/})[0]!); fireEvent.click(screen.getByText('Отметить выполненным')); await screen.findByRole('dialog', {name:'Подтвердить изменение шага'}); expect(done).toBe(false);
    fireEvent.click(screen.getByText('Нет')); expect(screen.queryByRole('dialog', {name:'Подтвердить изменение шага'})).not.toBeInTheDocument(); fireEvent.click(screen.getByText('Отметить выполненным')); await screen.findByRole('dialog', {name:'Подтвердить изменение шага'}); fireEvent.click(screen.getByText('Да, изменить шаг'));
    await screen.findByText('Вернуть в работу'); fireEvent.click(screen.getByText('Вернуть в работу')); await screen.findByRole('dialog', {name:'Подтвердить изменение шага'}); fireEvent.click(screen.getByText('Да, изменить шаг')); await screen.findByText('Отметить выполненным');
  });
  it('rejects malformed responses and surfaces version conflicts without losing the composer', async () => {
    await login(); server.use(http.post('*/api/cards', () => HttpResponse.json({ error: { code: 'VERSION_CONFLICT', fields: {}, retryable: false }, request_id: 'req' }, { status: 409 })));
    fireEvent.click(screen.getByText('Нужен отгул')); fireEvent.change(screen.getByLabelText('Первый день'), { target: { value: '2026-11-13' } }); fireEvent.click(screen.getByText('Подготовить карточку'));
    await screen.findByText('Карточка изменилась в другой вкладке. Загрузи актуальную версию.'); expect(screen.getByLabelText('Сообщение AI-buddy')).not.toBeDisabled(); fireEvent.click(within(screen.getByRole('form',{name:'Форма отгула'})).getByRole('button',{name:'Закрыть'}));
    server.use(http.get('*/api/onboarding', () => HttpResponse.json({ steps: 'invalid' }))); fireEvent.focus(window); await screen.findByText('Сервер вернул некорректные данные. Обнови страницу.', {}, { timeout: 4000 });
  });
});
