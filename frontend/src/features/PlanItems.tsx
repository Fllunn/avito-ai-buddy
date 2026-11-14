import { actorName,statusLabel,type Plan,type ServiceObject } from '../api/client';

export function PlanStep({step,readOnly,mark}:{step:Plan['steps'][number];readOnly:boolean;mark:(step:Plan['steps'][number])=>void}){
 return <article className={`plan-step ${step.status}`}><strong>{step.title}</strong><p>{statusLabel(step.status)}{step.blocked?' · Сначала выполни зависимость':''}{step.overdue?' · Просрочен':''}</p>
  <details><summary>Подробнее о шаге</summary><small>{step.source} · {new Date(step.updated_at).toLocaleString('ru-RU')} · {actorName(step.owner_id)}</small></details>
  {step.own&&step.closure==='manual'&&!step.blocked&&!readOnly&&<button onClick={()=>mark(step)}>{step.status==='done'?'Вернуть в работу':'Отметить выполненным'}</button>}
 </article>;
}
export function ServiceRequest({object,transfer,cancel}:{object:ServiceObject;transfer:(object:ServiceObject)=>void;cancel:(object:ServiceObject)=>void}){
 return <article className="plan-step"><strong>{object.id} · {statusLabel(object.status)}</strong><p>{object.resource_id??`${object.start_date} — ${object.end_date} · ${object.workdays} раб. дн.`}</p>
  <details><summary>Подробности заявки</summary><small>Согласующий: {actorName(object.approver_id)} · {new Date(object.updated_at).toLocaleString('ru-RU')}</small>{object.reason&&<p>{object.reason}</p>}</details>
  {object.service==='time_off'&&!['cancelled','rejected'].includes(object.status)&&<button onClick={()=>transfer(object)}>Перенести</button>}
  {!['cancelled','rejected'].includes(object.status)&&!(object.service==='access'&&object.status==='approved')&&<button onClick={()=>cancel(object)}>Подготовить отмену</button>}
 </article>;
}
