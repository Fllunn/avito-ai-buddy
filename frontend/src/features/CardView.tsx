import { useEffect, useState } from 'react';
import { Fieldset } from '../components/Primitives';
import { z } from 'zod';
import { actorName, errorText, cardSchema, confirmedSchema, queuedSchema, request, statusLabel, type Card } from '../api/client';
export function CardView({ card, changed, track }: { card: Card; changed: () => void; failed: (e: unknown) => void; track: (id: string) => void }) {
  const [cardError,setCardError]=useState('');
  const [editing, setEditing] = useState(false);
  const [busy, setBusy] = useState(false);
  const [resourceID, setResourceID] = useState(card.parameters.resource_id ?? '');
  const [justification, setJustification] = useState(card.parameters.justification ?? '');
  const [level, setLevel] = useState<string>(card.parameters.level ?? 'read');
  const [start, setStart] = useState(card.parameters.start_date ?? '');
  const [end, setEnd] = useState(card.parameters.end_date ?? '');
  const run = async (operation: () => Promise<void>) => { setCardError('');setBusy(true); try { await operation(); changed(); } catch (e) { setCardError(errorText(e)); changed(); } finally { setBusy(false); } };
  useEffect(()=>{if(!editing){setResourceID(card.parameters.resource_id??'');setJustification(card.parameters.justification??'');setLevel(card.parameters.level??'read');setStart(card.parameters.start_date??'');setEnd(card.parameters.end_date??'');}},[card.version,editing,card.parameters]);
  const attempt = card.attempt;
  return <article className="action-card" aria-label="Карточка подтверждения">
    <div className="card-title"><strong>{card.type.endsWith('cancel') ? (card.type === 'access_cancel' ? 'Отмена заявки' : 'Отмена отгула') : card.type === 'access_create' ? 'Заявка на доступ' : 'Запрос отгула'}</strong><span className="badge">Демо</span></div>
    {card.summary && <p>{card.summary.resource_name}{card.summary.start_label && `${card.summary.start_label} — ${card.summary.end_label} · ${card.summary.workdays} раб. дн.`}{card.summary.approver_id && ` · Согласующий: ${actorName(card.summary.approver_id)}`}</p>}
    <details className="demo-details"><summary>Технические сведения</summary><p>Демо · версия {card.version}</p>{card.parameters.expected_version && <p>Версия исходного запроса: {card.parameters.expected_version}</p>}</details>
    <dl>{Object.entries(card.parameters).filter(([name])=>name!=='expected_version').map(([name, value]) => <div key={name}><dt>{{ resource_id: 'Ресурс', level: 'Уровень', justification: 'Обоснование', start_date: 'Первый день', end_date: 'Последний день', request_id: 'Запрос', expected_version: 'Версия запроса' }[name] ?? name}</dt><dd>{name === 'level' ? ({ read: 'Чтение', edit: 'Редактирование', write: 'Запись' }[String(value)] ?? value) : name === 'resource_id' ? (card.summary?.resource_name ?? value) : value}</dd></div>)}</dl>
    <small>Действует до {new Date(card.expires_at).toLocaleString('ru-RU')}. Действие выполнится только после подтверждения.</small>
    {editing && <form onSubmit={(event) => { event.preventDefault(); void run(async () => {
      const changes = card.type === 'access_create' ? { resource_id: resourceID, level, justification } : { start_date: start, end_date: end };
      await request(`/cards/${card.id}`, z.object({ card: cardSchema }), 'PATCH', { expected_version: card.version, changes }); setEditing(false);
    }); }}>
      <Fieldset legend="Изменить параметры" error={cardError}>{card.type === 'access_create' ? <><label>Идентификатор ресурса<input value={resourceID} onChange={(e)=>setResourceID(e.target.value)} required /></label><label>Уровень<select value={level} onChange={(e) => setLevel(e.target.value)}><option value="read">Чтение</option><option value="edit">Редактирование</option><option value="write">Запись</option></select></label><label>Обоснование<textarea value={justification} onChange={(e) => setJustification(e.target.value)} required minLength={10} maxLength={500} /></label></> : <><label>Первый день<input type="date" value={start} onChange={(e) => setStart(e.target.value)} required /></label><label>Последний день<input type="date" value={end} onChange={(e) => setEnd(e.target.value)} required /></label></>}
      </Fieldset><button disabled={busy}>Сохранить новую версию</button><button type="button" onClick={() => setEditing(false)}>Закрыть редактор</button>
    </form>}
    {!editing && cardError && <p role="alert" className="field-error">{cardError}</p>}
    {card.state === 'draft' && <div className="button-row"><button disabled={busy || editing} className="primary" onClick={() => void run(async () => { const a = await request(`/cards/${card.id}/confirm`, confirmedSchema, 'POST', { expected_version: card.version }); track(a.job_id); })}>{card.type.endsWith('cancel') ? 'Подтвердить отмену' : card.type === 'access_create' ? 'Отправить заявку' : 'Отправить запрос на отгул'}</button>
      {card.type.endsWith('create') && <button disabled={busy} onClick={() => setEditing(true)}>Изменить</button>}
      <button disabled={busy} onClick={() => void run(async () => { await request(`/cards/${card.id}/discard`, z.object({ card: cardSchema }), 'POST', { expected_version: card.version }); })}>Не отправлять</button></div>}
    {attempt && <div role="status"><p>{statusLabel(attempt.state)} · проверено {new Date(attempt.as_of).toLocaleString('ru-RU')}</p>
      {(attempt.state === 'unknown' || attempt.state === 'manual_review' || attempt.state === 'unavailable') && <><p>{attempt.state === 'unavailable' ? 'Можно повторить с прежним ключом или проверить и закрыть прежнюю попытку перед новой карточкой.' : 'Неизвестно, выполнилось ли действие. Повторная отправка заблокирована до сверки.'}</p><button disabled={busy} onClick={() => void run(async () => { const j = await request(`/attempts/${attempt.id}/reconcile`, queuedSchema, 'POST', {}); track(j.job_id); })}>Проверить исход</button></>}
      {(attempt.state === 'unavailable' || attempt.state === 'not_created') && <button disabled={busy} onClick={() => void run(async () => { const j = await request(`/attempts/${attempt.id}/retry`, confirmedSchema, 'POST', {}); track(j.job_id); })}>Отправить заново с этими параметрами</button>}
    </div>}
  </article>;
}
