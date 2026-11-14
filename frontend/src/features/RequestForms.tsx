import { useState } from 'react';
import { z } from 'zod';
import { Fieldset } from '../components/Primitives';
import { ApiError, errorText, cardSchema, queuedSchema, clientID, request, type Resource, type Parameters, type ServiceObject } from '../api/client';
export function RequestForms({ kind, resources, created, close, defaults, transfer, track }: { kind: 'access' | 'time_off'; resources: Resource[]; defaults?: Parameters; transfer?: ServiceObject | null; track?: (id: string) => void; created: () => void; failed: (e: unknown) => void; close: () => void }) {
  const [formError, setFormError] = useState('');
  const [reapply, setReapply] = useState(false);
  const [resourceID, setResourceID] = useState(defaults?.resource_id ?? '');
  const [level, setLevel] = useState<string>(defaults?.level ?? 'read');
  const [justification, setJustification] = useState(defaults?.justification ?? '');
  const [start, setStart] = useState(defaults?.start_date ?? ''); const [end, setEnd] = useState(defaults?.end_date ?? ''); const [busy, setBusy] = useState(false);
  const chosen = resources.find((r) => r.id === resourceID);
  return <form className="action-card" aria-label={kind === 'access' ? 'Форма доступа' : 'Форма отгула'} onSubmit={(event) => { event.preventDefault(); setFormError(''); setBusy(true);
    const parameters = kind === 'access' ? { resource_id: resourceID, level, justification } : { start_date: start, end_date: end };
    void (reapply ? request('/dialogue/actions',queuedSchema,'POST',{client_message_id:clientID(),intent:kind==='access'?'access_request':'time_off_request',slots:{...parameters,query:'Подтверждаю повторную подачу'}}).then((job)=>{track?.(job.job_id);}) : transfer ? request('/dialogue/actions', queuedSchema, 'POST', { client_message_id: clientID(), intent: 'time_off_request', slots: { start_date: start, end_date: end, request_id: transfer.id } }).then((job) => { track?.(job.job_id); }) : request('/cards', z.object({ card: cardSchema }), 'POST', { type: kind === 'access' ? 'access_create' : 'time_off_create', parameters })).then(() => { created(); close(); }).catch((error: unknown)=>{if(error instanceof ApiError&&error.code==='REAPPLY_CONFIRMATION_REQUIRED')setReapply(true);setFormError(errorText(error));}).finally(() => setBusy(false));
  }}><div className="card-title"><h3>{transfer ? `Перенос ${transfer.id}` : kind === 'access' ? 'Подготовить доступ' : 'Подготовить отгул'}</h3><button type="button" onClick={close}>Закрыть</button></div>
    {transfer && <p>Перенос состоит из двух отдельных подтверждений. Для непересекающихся дат сначала создаётся новый запрос; при пересечении сначала отменяется старый, и новый может не пройти. Автоматического восстановления нет.</p>}
    <Fieldset legend={kind === 'access' ? 'Параметры доступа' : 'Даты отгула'} error={formError}>
    {kind === 'access' ? <><label>Ресурс<select required value={resourceID} onChange={(event) => { const r = resources.find((item) => item.id === event.target.value); setResourceID(event.target.value); setLevel(r?.default_level || 'read'); if (r?.kind !== 'task') setJustification('Для выполнения шагов демонстрационного плана адаптации'); else setJustification(''); }}><option value="">Выбрать ресурс</option>{resources.map((r) => <option key={r.id} value={r.id} disabled={r.kind === 'closed'}>{r.name}{r.kind === 'closed' ? ' — запрос недоступен' : ''}</option>)}</select></label>
      <label>Уровень<select value={level} onChange={(e) => setLevel(e.target.value)}>{(chosen?.levels ?? ['read']).map((l) => <option key={l} value={l}>{{ read: 'Чтение', edit: 'Редактирование', write: 'Запись' }[l] ?? l}</option>)}</select></label>
      <label>Рабочая задача / обоснование<textarea required minLength={10} maxLength={500} value={justification} onChange={(e) => setJustification(e.target.value)} /></label><p>Указывай только рабочую задачу. Если ресурс не относится к плану, обсуди его с наставником.</p></> : <><label>Первый день<input required type="date" value={start} onChange={(e) => { setStart(e.target.value); if (!end) setEnd(e.target.value); }} /></label><label>Последний день<input required type="date" value={end} onChange={(e) => setEnd(e.target.value)} /></label><p>Демонстрационные правила: 1–5 рабочих дней, не раньше следующего рабочего дня и не дальше 90 дней. Причину указывать не нужно. Все нарушения проверит сервер.</p></>}
    </Fieldset>
    <button disabled={busy} className="primary">{busy ? 'Проверяем параметры…' : reapply ? 'Подать повторно с этими параметрами' : 'Подготовить карточку'}</button><small>Это ещё не отправка. Следующий шаг — проверить карточку и подтвердить.</small>
  </form>;
}
