import { actorName, statusLabel, type Plan } from '../api/client';
import { Button } from '../components/Primitives';
import { Buddy } from '../components/Buddy';

export function NextStep({ plan, open, ask }: { plan?: Plan; open: () => void; ask: () => void }) {
  const step = plan?.next_steps[0];
  return <section className="main-step" aria-labelledby="next-step-title">
    <div><span className="eyebrow">ТВОЙ СЛЕДУЮЩИЙ ШАГ</span><h2 id="next-step-title">{!plan ? 'Загружаем твой план…' : plan.read_only ? 'Всё начнётся в день старта' : step?.title ?? (plan.waiting_on_others.length ? 'Сейчас ждём ответа от других' : 'Можно выдохнуть и свериться с планом')}</h2>
    <p>{plan?.read_only ? `Начало стажировки: ${plan.start_date ?? 'уточни у куратора'}. Пока можно изучить общие материалы.` : step ? 'Давай разберёмся вместе. Я помогу найти инструкцию и подготовить действие.' : 'Отсутствие следующего шага не означает, что вся адаптация завершена.'}</p>
    {step && !plan?.read_only ? <Button primary onClick={ask}>Разобраться с шагом <span aria-hidden="true">↗</span></Button> : <Button onClick={open}>Открыть план</Button>}</div><Buddy state="greeting" size={120} />
  </section>;
}
export function periodProgress(plan: Plan) {
  const own = plan.steps.filter((s) => s.own && s.period === plan.week);
  return {done: own.filter((s) => s.status === 'done').length, total: own.length};
}
export function OnboardingSummary({ plan, open }: { plan?: Plan; open: () => void }) {
  const progress = plan ? periodProgress(plan) : {done:0,total:0};
  return <aside className="onboarding-summary" aria-label="Сводка онбординга">
    <span className="eyebrow">В СВОЁМ ТЕМПЕ</span><h2>Твоя адаптация</h2><p>{plan ? `${plan.week}-я неделя` : 'Загружаем период…'}</p>
    {progress.total ? <><strong className="progress-count">{progress.done}<span> / {progress.total}</span></strong><p>твоих шагов этого периода выполнено</p><progress value={progress.done} max={progress.total} aria-label="Выполненные собственные шаги периода" /></> : <p>На этот период шагов пока нет.</p>}
    <Button onClick={open}>Мой онбординг <span aria-hidden="true">↗</span></Button>
    <div className="summary-waiting"><h3>Ждём от других</h3>{plan?.waiting_on_others.length ? plan.waiting_on_others.map((s)=><p key={s.id}><strong>{s.title}</strong><br/>{actorName(s.owner_id)} · {statusLabel(s.status)}</p>) : <p>Ожиданий от других сейчас нет.</p>}</div>
    {plan?.curator && <p>Куратор: {plan.curator.name}</p>}<small>Помощник рядом с тобой. Наставник — тоже: сложные вопросы лучше обсудить вместе.</small>
  </aside>;
}

export function WorkspaceContext({teamID,week}:{teamID:string|null;week?:number}){
 return <div className="personal-context"><span className="eyebrow">ЛИЧНОЕ ПРОСТРАНСТВО</span><p>{{payments:'Платежи',search:'Поиск'}[teamID??'']??'Команда не назначена'} · {week?`${week}-я неделя`:'Твой онбординг'}</p></div>;
}
