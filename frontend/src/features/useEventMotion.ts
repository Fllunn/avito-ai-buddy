import { useLayoutEffect,useRef,type RefObject } from 'react';
import { type Execution } from '../api/executions';
export function useEventMotion(root:RefObject<HTMLOListElement|null>,run:Execution,stale:boolean){
 const previous=useRef<{id:string;node:string;left:number;top:number;state:string}|null>(null);
 useLayoutEffect(()=>{
  const element=root.current?.querySelector<HTMLElement>('[aria-current="step"] .route-marker');
  if(!element)return;
  const reduced=window.matchMedia?.('(prefers-reduced-motion: reduce)');
  const rect=element.getBoundingClientRect();const last=previous.current;
  previous.current={id:run.id,node:run.current_node_id,left:rect.left,top:rect.top,state:run.state};
  let animation:Animation|undefined;
  if(last?.id===run.id&&!stale&&!reduced?.matches&&element.animate){
   if(last.node!==run.current_node_id&&run.state!=='completed')animation=element.animate([{transform:`translate(${last.left-rect.left}px,${last.top-rect.top}px)`},{transform:'translate(0,0)'}],{duration:220,easing:'ease-out'});
   else if(last.state!=='completed'&&run.state==='completed')animation=element.animate([{transform:'scale(1)'},{transform:'scale(1.05)'},{transform:'scale(1)'}],{duration:360});
  }
  const cancel=()=>animation?.cancel();reduced?.addEventListener('change',cancel);
  return()=>{cancel();reduced?.removeEventListener('change',cancel);};
 },[root,run.id,run.current_node_id,run.revision,run.state,stale]);
}
