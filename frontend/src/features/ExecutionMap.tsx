import { useMemo, useRef, useState } from 'react';
import { Buddy, buddyState } from '../components/Buddy';
import { useEventMotion } from './useEventMotion';
import { Dialog, Status, statusTone } from '../components/Primitives';
import { isActive, type Execution, type ExecutionEntry } from '../api/executions';
import { useJourneyPlayback } from './useJourneyPlayback';
const labels:Record<string,string>={context:'Разрешённый контекст',model:'Обработка вопроса',sources:'База знаний',validation:'Проверка параметров и прав',clarification:'Уточнение',confirmation:'Подтверждение',queue:'Очередь',service:'Обращение в сервис',persistence:'Сохранение результата',reconciliation:'Проверка исхода',answer:'Ответ'};
const nodeStates:Record<string,string>={pending:'Не начато',running:'Выполняется',waiting_user:'Проверь и подтверди',waiting_service:'Ждём ответ сервиса',succeeded:'Выполнено',skipped:'Пропущено',rejected:'Отказ',failed:'Ошибка',unknown:'Исход неизвестен'};
const runStates:Record<string,string>={queued:'В очереди',running:'Выполняется',waiting_user:'Ждём тебя',reconciling:'Уточняем исход',manual_review:'Нужна ручная проверка',completed:'Результат сохранён',rejected:'Действие отклонено',failed:'Не удалось завершить',cancelled:'Без отправки'};
const results:Record<string,string>={created:'Заявка создана. Ждём согласования.',submitted:'Запрос отправлен. Ждём решения руководителя.',cancelled:'Отмена сохранена.',unknown:'Неизвестно, выполнилось ли действие. Продолжаем сверку.',manual_review:'Нужна проверка ответственного. Повторная отправка заблокирована.',not_created:'Сверка подтвердила: операция не создана.',unavailable:'Сервис недоступен.',rejected:'Действие отклонено.',partial:'Выполнена только часть переноса.',fallback:'Продолжи через форму или кнопки.'};
function resultText(run:Execution){if(run.state==='cancelled'&&run.scenario!=='transfer')return 'Эта операция не отправлялась.';return run.result_code?results[run.result_code]:undefined;}
function stateText(run:Execution){return run.result_code==='partial'?'Перенос выполнен частично':runStates[run.state];}
type Playback = {playing:boolean;visible:string[]|null;current?:string};
function Nodes({entry,stale,detailed,playback}:{entry:ExecutionEntry;stale:boolean;detailed:boolean;playback?:Playback}){
 const r=entry.snapshot;const root=useRef<HTMLOListElement>(null);useEventMotion(root,r,stale||!!playback?.playing);
 const nodes=playback?.playing?r.nodes.filter(n=>playback.visible?.includes(n.id)):r.nodes;
 const ordered=playback?.playing?[...nodes].sort((a,b)=>playback.visible!.indexOf(a.id)-playback.visible!.indexOf(b.id)):nodes;
 return <ol ref={root} className={`route-nodes ${detailed?'route-detailed':''}`} aria-label="Этапы запроса">{ordered.map(n=><li key={n.id} data-state={n.state} data-replay={playback?.current===n.id||undefined} data-live={!stale&&isActive(r.state)&&r.current_node_id===n.id&&['running','waiting_service'].includes(n.state)||undefined} aria-current={r.current_node_id===n.id?'step':undefined}>
   <span className="route-marker" aria-hidden="true">{r.current_node_id===n.id?<Buddy size={40} state={buddyState(r.state,n.state)} stale={stale}/>:n.state==='succeeded'?'✓':n.state==='failed'||n.state==='rejected'?'!':n.state==='unknown'?'?':'·'}</span>
   <div><strong>{labels[n.label_code]}</strong><p>{n.state==='waiting_user'&&n.kind==='clarification'?'Уточни параметры':nodeStates[n.state]}</p>{detailed&&n.detail_code==='local'&&<small>Проверка выполнена локально, без отправки в сервис.</small>}{detailed&&n.started_at&&n.finished_at&&<small>{Math.max(0,(Date.parse(n.finished_at)-Date.parse(n.started_at))/1000).toLocaleString('ru-RU')} с · {new Date(n.finished_at).toLocaleTimeString('ru-RU')}</small>}</div>
 </li>)}</ol>;
}
function TransferBranches({entry,entries,stale,detailed,playback}:{entry:ExecutionEntry;entries:Record<string,ExecutionEntry>;stale:boolean;detailed:boolean;playback:Playback}){
 if(entry.snapshot.scenario!=='transfer')return null;
 return <div className="transfer-branches">{entry.snapshot.children?.map((child,index)=>{const prefix=`${child.id}/`;const childPlayback:Playback=playback.playing&&entries[child.id]?.events.length?{playing:true,visible:playback.visible?.filter(id=>id.startsWith(prefix)).map(id=>id.slice(prefix.length))??[],current:playback.current?.startsWith(prefix)?playback.current.slice(prefix.length):undefined}:{playing:false,visible:null};return <section key={child.id} aria-label={`Операция переноса ${index+1}`}><h3>Операция {index+1} · {stateText(child)}</h3><Nodes entry={{...entry,snapshot:child}} stale={stale} detailed={detailed} playback={childPlayback}/>{child.result_code&&<p>{resultText(child)??'Результат сохранён.'}</p>}</section>;})}</div>;
}
export function ExecutionMap({entry,entries={},now,replay=false,onReplayStart=()=>{}}:{entry:ExecutionEntry;entries?:Record<string,ExecutionEntry>;now:number;replay?:boolean;onReplayStart?:()=>void}){
 const [open,setOpen]=useState(false);const r=entry.snapshot;const stale=isActive(r.state)&&now-entry.lastSuccess>5000;
 const timeline=useMemo(()=>{if(r.scenario!=='transfer')return entry;let sequence=0;const nodes=(r.children??[]).flatMap(child=>child.nodes.map(node=>({...node,id:`${child.id}/${node.id}`})));const events=(r.children??[]).flatMap(child=>[...(entries[child.id]?.events??[])].sort((a,b)=>a.sequence-b.sequence).map(event=>({...event,sequence:++sequence,node:{...event.node,id:`${child.id}/${event.node.id}`}})));return {...entry,snapshot:{...r,nodes},events};},[entry,entries,r]);
 const [replayEnabled]=useState(replay);const playback=useJourneyPlayback(timeline,replayEnabled,stale,onReplayStart);
 const activeNode=r.nodes.find(n=>n.id===r.current_node_id);
 return <article className="execution-map" aria-label={`Маршрут: ${{orientation:'Вопрос и ориентирование',access:'Доступ',time_off:'Отгул',transfer:'Перенос'}[r.scenario]}`}>
 <header><span className="eyebrow">МАРШРУТ ЗАПРОСА</span><span role="status"><Status tone={statusTone(stale?'stale':r.result_code==='partial'?'partial':r.state)}>{stale?'Нет свежих данных':stateText(r)}</Status></span></header>
 {playback.playing&&<p className="journey-caption">Показываем пройденный путь · текущий статус указан выше</p>}
 {playback.playing&&activeNode&&r.state!=='completed'&&<p className="journey-live">Сейчас: {labels[activeNode.label_code]} · {nodeStates[activeNode.state]}</p>}
 {r.scenario!=='transfer'&&<Nodes entry={entry} stale={stale} detailed={false} playback={playback}/>}<TransferBranches entry={entry} entries={entries} stale={stale} detailed={false} playback={playback}/>
 {r.scenario==='transfer'&&r.state==='completed'&&<p>Обе операции переноса сохранены. Статус нового запроса смотри отдельно.</p>}
 {resultText(r)&&<p>{resultText(r)}</p>}
 {stale&&<p>Последняя проверка: {new Date(entry.lastSuccess).toLocaleTimeString('ru-RU')}. Сохранённые данные остаются доступны.</p>}
 {!r.detail_complete&&<p>Часть промежуточных сведений недоступна.</p>}
 <button onClick={()=>setOpen(true)}>Показать маршрут</button>
 <Dialog title="Маршрут запроса" open={open} close={()=>setOpen(false)}><p><Status tone={statusTone(stale?'stale':r.result_code==='partial'?'partial':r.state)}>{stale?'Нет свежих данных':stateText(r)}</Status></p>{playback.playing&&<p className="journey-caption">Показываем пройденный путь · текущий статус указан выше</p>}{playback.playing&&activeNode&&r.state!=='completed'&&<p className="journey-live">Сейчас: {labels[activeNode.label_code]} · {nodeStates[activeNode.state]}</p>}{r.scenario!=='transfer'&&<Nodes entry={entry} stale={stale} detailed playback={playback}/>}<TransferBranches entry={entry} entries={entries} stale={stale} detailed playback={playback}/>{r.result_code&&<p>{resultText(r)??'Ответ сохранён.'}</p>}<p>Показаны сохранённые факты. Завершение отправки не означает согласование заявки.</p></Dialog>
 </article>;
}
