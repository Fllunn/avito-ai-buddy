import { useEffect, useState } from 'react';
import { QueryClient, QueryClientProvider, useQuery, useQueryClient } from '@tanstack/react-query';
import { z } from 'zod';
import { Dialog } from './components/Primitives';
import { PlanStep, ServiceRequest } from './features/PlanItems';
import { NextStep, OnboardingSummary, WorkspaceContext } from './features/WorkspaceOverview';
import { ExecutionRoutes } from './features/ExecutionRoutes';
import { useExecutions } from './features/useExecutions';
import { ChatMessages } from './features/ChatMessages';
import { Buddy } from './components/Buddy';
import { Icon } from './components/Icon';
import { Logo } from './components/Logo';
import { CardView } from './features/CardView';
import { RequestForms } from './features/RequestForms';
import { useUI } from './stores/ui';
import { actorName, cardSchema, chatSchema, clientID, dialogueResultSchema, errorText, jobSchema, meSchema, planSchema, profileSchema, queuedSchema, request, resetSession, sessionGeneration, statusLabel, stepConfirmationSchema, stepSchema, type Resource, type ServiceObject, type Parameters } from './api/client';

const profiles = [['alina', 'Алина · стажёр · Платежи'], ['maxim', 'Максим · стажёр · Поиск'], ['igor', 'Игорь · наставник'], ['sergey', 'Сергей · наставник'], ['olga', 'Ольга · руководитель'], ['anna', 'Анна · руководитель'], ['daria', 'Дарья · HR BP']];
export function App() {
  const [client] = useState(() => new QueryClient({ defaultOptions: { queries: { retry: false, refetchOnWindowFocus: false } } }));
  return <QueryClientProvider client={client}><Workspace /></QueryClientProvider>;
}
function Workspace() {
  const queryClient = useQueryClient();
  const ui = useUI();
  const [epoch, setEpoch] = useState(sessionGeneration()); const [switching, setSwitching] = useState(false);
  const [planOpen, setPlanOpen] = useState(false);
  const [transfer, setTransfer] = useState<ServiceObject | null>(null);
  const [formDefaults, setFormDefaults] = useState<Parameters>({});
  const [error, setError] = useState(''); const [jobs, setJobs] = useState<string[]>([]); const [resources, setResources] = useState<Resource[]>([]);
  const [confirmation, setConfirmation] = useState<{ id: string; step: string; summary: string } | null>(null);
  const me = useQuery({ queryKey: ['me', epoch], queryFn: () => request('/me', meSchema), enabled: !switching });
  const profile = !switching ? me.data?.profile : undefined;
  const executions = useExecutions(epoch, !!profile, jobs.length > 0);
  const plan = useQuery({ queryKey: ['plan', epoch], queryFn: () => request('/onboarding', planSchema), enabled: !!profile });
  const chat = useQuery({ queryKey: ['chat', epoch], queryFn: () => request('/chat', chatSchema), enabled: !!profile });
  const refresh = () => { void queryClient.invalidateQueries({queryKey:['executions',epoch]}); void queryClient.invalidateQueries({ queryKey: ['plan', epoch] }); void queryClient.invalidateQueries({ queryKey: ['chat', epoch] }); };
  const track = (id: string) => setJobs((old) => old.includes(id) ? old : [...old, id]);
  const failed = (e: unknown) => setError(errorText(e));
  const run = async (operation: () => Promise<void>) => { setError(''); const current = sessionGeneration(); try { await operation(); if (current === sessionGeneration()) refresh(); } catch (e) { if (current === sessionGeneration()) failed(e); } };
  const statuses = async () => { const result = await request('/requests/refresh', queuedSchema, 'POST', { kind: 'access' }); track(result.job_id); };
  useEffect(() => {
    if (!profile) return;
    let active = true;
    void request('/requests/refresh', queuedSchema, 'POST', { kind: 'access' }).then((j) => { if (active) track(j.job_id); }).catch((e: unknown) => { if (active) failed(e); });
    const focus = () => { if (active) void run(statuses); };
    window.addEventListener('focus', focus);
    return () => { active = false; window.removeEventListener('focus', focus); };
  // The effect runs only when the authenticated identity changes.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [profile?.id, epoch]);
  useEffect(() => {
    if (!profile) return;
    for (const card of chat.data?.active_cards ?? []) if (card.attempt && ['queued', 'sending', 'unknown'].includes(card.attempt.state)) track(card.attempt.job_id);
  }, [chat.data, profile]);
  useEffect(() => {
    if (!jobs.length || !profile) return;
    let active = true;
    const current = sessionGeneration();
    const timer = window.setInterval(() => { for (const id of jobs) void request(`/jobs/${id}`, jobSchema).then((job) => {
      if (!active || current !== sessionGeneration() || job.state === 'queued' || job.state === 'running') return;
      setJobs((old) => old.filter((value) => value !== id));
      if (job.error) setError(errorText(new Error(job.error)));
      const result = dialogueResultSchema.safeParse(job.result);
      if (result.success) for (const item of result.data.items) {
       if (item.resources) {setResources(item.resources);ui.setForm('access');}
       if (item.access_form) {setFormDefaults(item.access_form);ui.setForm('access');}
       if (item.time_off_form) {setFormDefaults(item.time_off_form);ui.setForm('time_off');}
       if (item.related_job) track(item.related_job);
      }
      refresh();
    }).catch((e: unknown) => { if (active && current === sessionGeneration()) { failed(e); setJobs((old) => old.filter((value) => value !== id)); } }); }, 1000);
    return () => { active = false; window.clearInterval(timer); };
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [jobs, profile?.id, epoch]);
  const changeProfile = async (id: string) => {
    resetSession(); setEpoch(sessionGeneration()); setSwitching(true); setPlanOpen(false); setJobs([]); setResources([]); setConfirmation(null); setTransfer(null); setFormDefaults({}); setError(''); ui.reset();
    await queryClient.cancelQueries(); queryClient.clear();
    try { if (id) await request('/demo/session', z.object({ profile: profileSchema }), 'POST', { user_id: id }); else await request('/session', z.undefined(), 'DELETE'); }
    catch (e) { failed(e); } finally { setSwitching(false); }
  };
  const action = async (intent: string) => { const job = await request('/dialogue/actions', queuedSchema, 'POST', { client_message_id: clientID(), intent, slots: {} }); track(job.job_id); };
  const prepareCancel = async (object: ServiceObject) => { await request('/cards', z.object({ card: cardSchema }), 'POST', { type: object.service === 'access' ? 'access_cancel' : 'time_off_cancel', parameters: { request_id: object.id, expected_version: object.version } }); };
  return <div className="trainee-app live-app"><header className="topbar"><Logo /><span className="demo-label">Демонстрационный прототип</span><label className="profile-label">Демо-профиль<select aria-label="Демо-профиль" disabled={switching} value={profile?.id ?? ''} onChange={(event) => void changeProfile(event.target.value)}><option value="">Выбрать профиль</option>{profiles.map(([id, label]) => <option key={id} value={id}>{label}</option>)}</select></label>{profile && <button onClick={() => void changeProfile('')}>Выйти</button>}</header>
    {!profile ? <main className="empty-chat"><div className="welcome"><Buddy state="greeting" size={140} /><h1>Привет! С чего начнём?</h1><p>Выбери синтетический демо-профиль, чтобы открыть чат и свой план адаптации.</p><p role="status">{switching ? 'Меняем профиль…' : me.isPending ? 'Проверяем сессию…' : 'Вход выполняется через выбор профиля сверху.'}</p>{error && <p role="alert">{error}</p>}</div></main> : <main className="workspace">
    <div className="workspace-main"><WorkspaceContext teamID={profile.team_id} week={plan.data?.week}/><NextStep plan={plan.data} open={() => setPlanOpen(true)} ask={() => void run(() => action("next_step"))} /><section className="chat-panel" aria-label="Чат с AI-buddy"><div className="section-heading"><div><h1>Привет, {profile.name.split(' ')[0]}!</h1><p>Твой помощник в адаптации</p></div><details className="demo-details"><summary>О демо</summary><span className="badge">{chat.data?.llm_mode === 'openrouter' ? 'OpenRouter' : 'Детерминированный fake'}</span><p>Синтетические данные и демонстрационные правила.</p></details></div>
    {error && <div className="error-banner" role="alert">{error}<button aria-label="Закрыть ошибку" onClick={() => setError('')}>×</button></div>}
    {(chat.error || plan.error) && <div role="alert">{errorText(chat.error ?? plan.error)}<button onClick={refresh}>Повторить загрузку</button></div>}
    <div className="messages"><div role="log" aria-label="История диалога" aria-relevant="additions"><ChatMessages messages={chat.data?.items ?? []} onAction={intent=>void run(()=>action(intent))}/></div>
    {chat.data?.active_cards.map((card) => <CardView key={card.id} card={card} changed={refresh} failed={failed} track={track} />)}
    {ui.form && <RequestForms key={`${ui.form}:${JSON.stringify(formDefaults)}`} defaults={formDefaults} transfer={transfer} track={track} kind={ui.form} resources={resources} created={refresh} failed={failed} close={() => {ui.setForm(null);setTransfer(null);}} />}
    {executions.unavailable && <p className="notice">Сведения маршрутов сейчас недоступны. Заявки и подтверждения продолжают работать.</p>}
    <ExecutionRoutes key={epoch} entries={executions.entries} now={executions.now}/>
    {jobs.length > 0 && Object.keys(executions.entries).length===0 && <p className="waiting" role="status">Запрос принят. Проверяем его состояние — можно продолжать работу.</p>}</div>
    <div className="quick-actions"><button onClick={() => void run(() => action('next_step'))}>С чего начать?</button><button disabled={!me.data?.capabilities.service_actions} onClick={() => { setFormDefaults({});ui.setForm('access'); void run(() => action('access_request')); }}>Нужен доступ</button><button disabled={!me.data?.capabilities.service_actions} onClick={() => {setTransfer(null);setFormDefaults({});ui.setForm('time_off');}}>Нужен отгул</button></div>
    <form className="composer" onSubmit={(event) => { event.preventDefault(); const text = ui.draft.trim(); if (!text) return; ui.setDraft(''); void run(async () => { const result = await request('/chat/messages', queuedSchema, 'POST', { client_message_id: clientID(), text }); track(result.job_id); }); }}><input aria-label="Сообщение AI-buddy" placeholder="Напиши вопрос. Без чувствительных данных" value={ui.draft} onChange={(event) => ui.setDraft(event.target.value)} maxLength={4000} /><button aria-label="Отправить" disabled={!ui.draft.trim()}><Icon name="send" size={18} /></button><small>Действия выполняются только после подтверждения. Правила и данные демонстрационные.</small></form>
    </section></div>
    <OnboardingSummary plan={plan.data} open={() => setPlanOpen(true)} /><button className="mobile-plan" onClick={() => setPlanOpen(true)}>Мой онбординг</button><Dialog title="Мой онбординг" open={planOpen} close={() => setPlanOpen(false)}><section className="onboarding-panel" aria-label="Мой онбординг"><div className="section-heading"><div><h2>Мой онбординг</h2><p>{plan.data?.week}-я неделя · {{payments:'Платежи',search:'Поиск'}[profile.team_id ?? ''] ?? 'Команда не назначена'}</p></div><button onClick={() => void run(statuses)}>Проверить статусы</button></div>
    {plan.data?.read_only && <p role="status">До начала стажировки план доступен только для чтения.</p>}
    {plan.data?.notices?.map((notice)=><p className="notice" key={notice}>{notice}</p>)}{plan.data?.curator && <p>Куратор: {plan.data.curator.name}</p>}
    <h3>Следующие шаги</h3>{plan.data?.next_steps.map((step) => <p className="next-step" key={step.id}>{step.title}</p>)}
    <h3>Ждём от других</h3>{plan.data?.waiting_on_others.map((step) => <p key={step.id}>{step.title} · {actorName(step.owner_id)} · {statusLabel(step.status)}</p>)}
    <h3>Все шаги</h3>{plan.data?.steps.map(step=><PlanStep key={step.id} step={step} readOnly={plan.data?.read_only??false} mark={step=>void run(async()=>{const c=await request(`/steps/${step.id}/confirmation`,stepConfirmationSchema,'POST',{expected_version:step.version,target_state:step.status==='done'?'in_progress':'done'});setConfirmation({id:c.confirmation_id,step:step.id,summary:c.summary});})}/>)}
    {confirmation && <Dialog title="Подтвердить изменение шага" open={!!confirmation} close={() => setConfirmation(null)}><p>{confirmation.summary}</p><button onClick={() => void run(async () => { await request(`/steps/${confirmation.step}/confirm`, z.object({ step: stepSchema.omit({ blocked: true, overdue: true, own: true }) }), 'POST', { confirmation_id: confirmation.id }); setConfirmation(null); })}>Да, изменить шаг</button><button onClick={() => setConfirmation(null)}>Нет</button></Dialog>}
    <h3>Мои заявки и запросы</h3>{plan.data && !plan.data.requests.length && <div className="empty-requests"><Buddy state="greeting" size={70}/><p>Заявок пока нет. Здесь появятся сохранённые результаты доступа и отгула.</p></div>}{plan.data?.requests.map(object=><ServiceRequest key={object.id} object={object} transfer={object=>{setTransfer(object);setFormDefaults({start_date:object.start_date,end_date:object.end_date});ui.setForm('time_off');setPlanOpen(false);}} cancel={object=>{setPlanOpen(false);void run(()=>prepareCancel(object));}}/>)}
    <small>Состояния обновляются при открытии страницы и кнопкой проверки. После отправки отгула самостоятельно предупреди наставника.</small>
    </section></Dialog></main>}
  </div>;
}
