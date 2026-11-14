import { useEffect, useRef, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { request, sessionGeneration } from '../api/client';
import { executionsSchema, isActive, readExecution, type ExecutionEntry } from '../api/executions';
export function useExecutions(epoch:number,enabled:boolean,busy:boolean){
  const entries=useRef<Record<string,ExecutionEntry>>({});const generation=useRef(epoch);
  if(generation.current!==epoch){generation.current=epoch;entries.current={};}
  const [now,setNow]=useState(Date.now());
  const query=useQuery({queryKey:['executions',epoch],enabled,retry:false,refetchOnWindowFocus:true,
    refetchInterval:(q)=>busy||Object.values(q.state.data?.entries??{}).some(e=>isActive(e.snapshot.state))?1000:false,
    queryFn:async()=>{
      const current=sessionGeneration();
      const [recent,active]=await Promise.all([request('/executions?limit=20',executionsSchema),request('/executions?active=true&limit=100',executionsSchema)]);
      const activeItems=[...active.items]; let cursor=active.next_cursor; for(let i=0;cursor&&i<100;i++){ const page=await request(`/executions?active=true&limit=100&cursor=${encodeURIComponent(cursor)}`,executionsSchema);activeItems.push(...page.items);if(page.next_cursor===cursor)throw new Error('INVALID_CURSOR');cursor=page.next_cursor;}
      const ids=new Set([...recent.items,...activeItems].map(r=>r.id));
      const next={...entries.current};let unavailable=false;
      await Promise.all([...ids].map(async id=>{try{next[id]=await readExecution(id,next[id]);}catch{unavailable=true;}}));
      // A recent continuation can refer to an older request outside the history page.
      for(let depth=0;depth<4;depth++){
        const parents=new Set(Object.values(next).flatMap(e=>e.snapshot.links.filter(l=>(l.kind==='parent'||l.kind==='operation_parent')&&!next[l.id]).map(l=>l.id)));
        if(!parents.size)break;
        await Promise.all([...parents].map(async id=>{try{next[id]=await readExecution(id,undefined);}catch{unavailable=true;}}));
        if(current!==sessionGeneration())throw new Error('SESSION_CHANGED');
      }
      if(current!==sessionGeneration()||epoch!==generation.current)throw new Error('SESSION_CHANGED');
      entries.current=next;return {entries:next,unavailable};
    }});
  useEffect(()=>{if(!enabled)return;const timer=window.setInterval(()=>setNow(Date.now()),1000);return()=>window.clearInterval(timer);},[enabled]);
  return {entries:query.data?.entries??{},unavailable:query.isError||query.data?.unavailable,now};
}
