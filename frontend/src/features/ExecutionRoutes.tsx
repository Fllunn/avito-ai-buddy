import { useState } from 'react';
import { type ExecutionEntry } from '../api/executions';
import { ExecutionMap } from './ExecutionMap';
export function ExecutionRoutes({entries,now}:{entries:Record<string,ExecutionEntry>;now:number}){
 const [selected,setSelected]=useState('');
 const sorted=Object.values(entries).filter(e=>!e.snapshot.links.some(l=>(l.kind==='parent'||l.kind==='operation_parent')&&entries[l.id])).sort((a,b)=>b.snapshot.started_at.localeCompare(a.snapshot.started_at)||b.snapshot.id.localeCompare(a.snapshot.id));
 const current=entries[selected]??sorted[0];
 if(!current)return null;
 return <section aria-label="Выполнение запросов">{sorted.length>1&&<label className="route-selector">Запрос<select aria-label="Запрос" value={current.snapshot.id} onChange={e=>setSelected(e.target.value)}>{sorted.map(e=><option key={e.snapshot.id} value={e.snapshot.id}>{{orientation:'Вопрос',access:'Доступ',time_off:'Отгул',transfer:'Перенос'}[e.snapshot.scenario]} · {new Date(e.snapshot.started_at).toLocaleTimeString('ru-RU')}</option>)}</select></label>}<ExecutionMap entry={current} now={now}/></section>;
}
