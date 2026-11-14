import { z } from 'zod';

const id = z.string().min(1);
const instant = z.iso.datetime({ offset: true });
const version = z.number().int().positive();
export const profileSchema = z.object({ id, name: id, role: z.enum(['intern', 'buddy', 'manager', 'hr']), team_id: id.nullable(), start_date: z.iso.date(), buddy_id: id.nullable(), manager_id: id.nullable(), curator_id: id.nullable(), policy_version: version });
export const meSchema = z.object({ profile: profileSchema, capabilities: z.object({ demo: z.boolean(), service_actions: z.boolean() }) });
export const objectSchema = z.object({ service: z.enum(['access', 'time_off']), id, user_id: id, resource_id: id.optional(), level: id.optional(), justification: z.string().optional(), start_date: z.iso.date().optional(), end_date: z.iso.date().optional(), workdays: z.number().int().positive().optional(), approver_id: id, status: z.enum(['created', 'submitted', 'in_review', 'approved', 'rejected', 'cancelled']), reason: z.string().nullable(), version, created_at: instant, updated_at: instant });
const resultSchema = z.object({ kind: z.enum(['success', 'rejected', 'unavailable', 'unknown']), object: objectSchema.optional(), code: z.string().optional(), source: z.enum(['service', 'local_validation']) });
export const attemptSchema = z.object({ id, card_id: id, card_version: version, number: version, parameters: z.record(z.string(), z.union([z.string(), z.number()])), state: z.enum(['queued', 'sending', 'succeeded', 'rejected', 'unavailable', 'unknown', 'manual_review', 'not_created']), result: resultSchema.optional(), created_at: instant, as_of: instant, next_check_at: instant.nullable().optional(), job_id: id });
export const parametersSchema = z.object({ resource_id: id.optional(), level: z.enum(['read', 'edit', 'write']).optional(), justification: z.string().optional(), start_date: z.iso.date().optional(), end_date: z.iso.date().optional(), request_id: id.optional(), expected_version: version.optional() }).strict();
export const cardSchema = z.object({ execution_ids: z.array(id).optional(), execution_id: id.optional(), id, user_id: id, type: z.enum(['access_create', 'access_cancel', 'time_off_create', 'time_off_cancel']), version, state: z.enum(['draft', 'confirmed', 'discarded', 'expired', 'replaced']), expires_at: instant, updated_at: instant, parameters: parametersSchema, summary: z.object({ resource_name: z.string().optional(), approver_id: z.string().optional(), workdays: z.number().int().positive().optional(), start_label: z.string().optional(), end_label: z.string().optional(), demo: z.boolean() }).optional(), attempt: attemptSchema.optional() });
export const stepSchema = z.object({ id, intern_id: id, code: id, title: id, period: version, owner_id: id, closure: z.enum(['manual', 'service']), status: z.enum(['not_started', 'in_progress', 'waiting', 'done', 'attention']), dependencies: z.array(id), source: z.string(), updated_at: instant, version, blocked: z.boolean(), overdue: z.boolean(), own: z.boolean() });
export const planSchema = z.object({ notices: z.array(z.string()).optional(), start_date: z.iso.date().optional(), curator: z.object({id,name:id,role:id,demo:z.boolean()}).optional(), steps: z.array(stepSchema), next_steps: z.array(stepSchema), waiting_on_others: z.array(stepSchema), requests: z.array(objectSchema), week: version, read_only: z.boolean(), as_of: instant, refresh_job_id: id.optional() });
const messageSchema = z.object({ execution_ids: z.array(id).optional(), execution_id: id.optional(), id: z.number().int(), sequence: z.number().int().positive(), client_message_id: id.nullable(), role: z.enum(['user', 'assistant', 'system']), kind: id, metadata: z.lazy(() => z.union([dialogueResultSchema, z.object({ code: id, mode: id }), z.null()])).optional(), text: z.string(), intent: z.string(), slots: z.record(z.string(), z.string()), form_required: z.boolean(), created_at: instant });
export const chatSchema = z.object({ items: z.array(messageSchema), next_cursor: z.number().int().nonnegative(), active_cards: z.array(cardSchema), llm_mode: z.enum(['fake', 'openrouter']) });
export const resourceSchema = z.object({ id, name: id, kind: id, levels: z.array(z.string()), default_level: z.string(), approver_id: z.string(), recommended_teams: z.array(z.string()), synonyms: z.array(z.string()) });
const sourceSchema = z.object({ id, title: id, url: z.string(), stale: z.boolean(), demo: z.boolean(), owner_id: id });
const answerSchema = z.object({ text: z.string(), sources: z.array(sourceSchema), code: id, mode: id, contact_id: id.optional() });
const contactSchema = z.object({ text: z.string(), contact: z.object({ id, name: id, role: id, channel: z.string().optional() }).passthrough().optional(), step_state: z.string().optional() });
export const dialogueResultSchema = z.object({ items: z.array(z.object({ prompt: z.enum(['next_intent','reapply','confirm_step']).optional(), related_job: id.optional(), resources: z.array(resourceSchema).optional(), card: cardSchema.optional(), answer: answerSchema.optional(), contact: contactSchema.optional(), fields: z.record(z.string(), z.string()).optional(), access_form: parametersSchema.optional(), time_off_form: parametersSchema.optional(), plan: planSchema.optional() })), mode: id });
export const jobSchema = z.object({ execution_ids: z.array(id).optional(), execution_id: id.optional(), id, state: z.enum(['queued', 'running', 'completed', 'failed']), result: z.union([dialogueResultSchema, resultSchema, z.object({ code: z.string(), mode: z.string() }), z.null()]).optional(), error: z.string().optional() });
export const queuedSchema = z.object({ execution_ids: z.array(id).optional(), execution_id: id.optional(), job_id: id, message_id: z.number().optional(), message_ids: z.array(z.number()).optional() });
export const confirmedSchema = z.object({ execution_ids: z.array(id).optional(), execution_id: id.optional(), attempt_id: id, job_id: id, state: id });
export const stepConfirmationSchema = z.object({ confirmation_id: id, summary: id, expires_at: instant });
export type Card = z.infer<typeof cardSchema>;
export type Parameters = z.infer<typeof parametersSchema>;
export type Plan = z.infer<typeof planSchema>;
export type ServiceObject = z.infer<typeof objectSchema>;
export type Resource = z.infer<typeof resourceSchema>;

let generation = 0;
const controllers = new Set<AbortController>();
export function resetSession() { generation++; for (const controller of controllers) controller.abort(); controllers.clear(); }
export function sessionGeneration() { return generation; }
export class ApiError extends Error {
  constructor(public code: string, public status: number, public fields: Record<string,string> = {}) { super(code); }
}
const errorSchema = z.object({ error: z.object({ code: z.string(), fields: z.record(z.string(), z.string()), retryable: z.boolean() }), request_id: id });
export async function request<T>(path: string, schema: z.ZodType<T>, method = 'GET', body?: unknown): Promise<T> {
  const current = generation;
  const controller = new AbortController(); controllers.add(controller);
  try {
    const response = await fetch(`${import.meta.env.VITE_API_BASE_URL ?? ''}/api${path}`, { method, credentials: 'include', signal: controller.signal, headers: { 'Content-Type': 'application/json' }, ...(body === undefined ? {} : { body: JSON.stringify(body) }) });
    if (current !== generation) throw new ApiError('SESSION_CHANGED', 0);
    const raw: unknown = response.status === 204 ? undefined : await response.json();
    if (current !== generation) throw new ApiError('SESSION_CHANGED', 0);
    if (!response.ok) { const parsed = errorSchema.safeParse(raw); throw new ApiError(parsed.success ? parsed.data.error.code : 'INVALID_RESPONSE', response.status, parsed.success ? parsed.data.error.fields : {}); }
    const parsed = schema.safeParse(raw);
    if (!parsed.success) throw new ApiError('INVALID_RESPONSE', 0);
    return parsed.data;
  } finally { controllers.delete(controller); }
}
export const clientID = () => crypto.randomUUID();
export function errorText(error: unknown) {
  if (!(error instanceof ApiError)) return 'Не удалось связаться с сервером. Попробуй ещё раз.';
  const labels: Record<string, string> = { UNAUTHENTICATED: 'Выбери демо-профиль для входа.', NOT_ALLOWED: 'Это действие недоступно. Уточни у наставника или куратора.', NOT_FOUND: 'Объект не найден или недоступен.', VALIDATION_ERROR: 'Проверь параметры формы.', VERSION_CONFLICT: 'Карточка изменилась в другой вкладке. Загрузи актуальную версию.', CARD_EXPIRED: 'Срок карточки истёк. Подготовь новую и подтверди её.', CONFLICTING_ATTEMPT: 'Предыдущий исход ещё уточняется. Сначала проверь попытку.', ALREADY_GRANTED: 'Доступ уже выдан. Если ресурс не открывается, обратись к наставнику.', DUPLICATE_OPEN_REQUEST: 'Уже есть открытая заявка на этот ресурс.', OVERLAPPING_REQUEST: 'На эти дни уже есть активный запрос.', LEVEL_NOT_ALLOWED: 'Выбери разрешённый уровень доступа.', CANCELLATION_TOO_LATE: 'Отмена возможна только до первого дня. Обратись к руководителю.', INVALID_RESPONSE: 'Сервер вернул некорректные данные. Обнови страницу.', REAPPLY_CONFIRMATION_REQUIRED: 'Предыдущий запрос отклонён. Проверь причину и отдельно подтверди повторную подачу.', MESSAGE_ID_CONFLICT: 'Этот запрос уже использован с другими параметрами.', SESSION_CHANGED: 'Профиль сменился. Повтори действие.' };
  return (labels[error.code] ?? 'Сервис временно недоступен. Сохранённые данные остаются на экране.') + (['VALIDATION_ERROR','REAPPLY_CONFIRMATION_REQUIRED'].includes(error.code) ? ' ' + Object.values(error.fields).join(' ') : '');
}
export const statusLabel = (status: string) => ({ created: 'Заявка создана', submitted: 'Отправлен', in_review: 'На рассмотрении', approved: 'Согласован', rejected: 'Отклонён', cancelled: 'Отменён', done: 'Выполнен', waiting: 'Ожидание', attention: 'Нужно внимание', in_progress: 'В работе', not_started: 'Не начат', queued: 'В очереди', sending: 'Отправляется', unknown: 'Исход уточняется', manual_review: 'Нужна ручная проверка', unavailable: 'Сервис недоступен', not_created: 'Не создан', succeeded: 'Результат сохранён' }[status] ?? status);

export const actorName = (id: string) => ({alina:'Алина Соколова',maxim:'Максим Орлов',igor:'Игорь Белов',sergey:'Сергей Ким',olga:'Ольга Миронова',anna:'Анна Петрова',daria:'Дарья Лебедева',timur:'Тимур Гаранин'}[id] ?? id);
export const intentLabel = (intent: string) => ({next_step:'С чего начать?',access_request:'Нужен доступ',time_off_request:'Нужен отгул',find_doc:'Найти инструкцию',who_to_ask:'К кому обратиться?',time_off_info:'Правила отгула',status:'Проверить статусы',cancel:'Подготовить отмену',mark_done:'Отметить шаг',card_edit:'Изменить карточку',card_confirm:'Подтвердить',card_cancel:'Не отправлять',smalltalk:'Приветствие',out_of_scope:'Другой вопрос',unclear:'Уточнить вопрос'}[intent] ?? 'Запрос помощнику');

export type Chat = z.infer<typeof chatSchema>;
