import { z } from 'zod';
import { request, sessionGeneration } from './client';
const instant = z.iso.datetime({offset:true});
export const executionStateSchema = z.enum(['queued','running','waiting_user','reconciling','manual_review','completed','rejected','failed','cancelled']);
const kind = z.enum(['context','model','sources','validation','clarification','confirmation','queue','service','persistence','reconciliation','answer']);
const nodeSchema = z.object({id:z.string().min(1),kind,state:z.enum(['pending','running','waiting_user','waiting_service','succeeded','skipped','rejected','failed','unknown']),label_code:kind,detail_code:z.string().optional(),service_code:z.enum(['knowledge','access','time_off']).optional(),started_at:instant.optional(),finished_at:instant.optional()});
const executionCoreSchema=z.object({id:z.string().min(1),scenario:z.enum(['orientation','access','time_off','transfer']),state:executionStateSchema,current_node_id:z.string(),revision:z.number().int().positive(),last_sequence:z.number().int().nonnegative(),started_at:instant,updated_at:instant,result_code:z.string().optional(),detail_complete:z.boolean(),nodes:z.array(nodeSchema),edges:z.array(z.object({from:z.string(),to:z.string()})),links:z.array(z.object({kind:z.string(),id:z.string(),version:z.number().int().optional()}))});
export const executionSchema=executionCoreSchema.extend({children:z.array(executionCoreSchema).optional()});
const eventSchema=z.object({sequence:z.number().int().positive(),node:nodeSchema,state:executionStateSchema,at:instant});
export const executionPageSchema=z.object({snapshot:executionSchema,events:z.array(eventSchema),next_sequence:z.number().int().nonnegative(),has_more:z.boolean(),as_of:instant,detail_complete:z.boolean()});
export const executionsSchema=z.object({items:z.array(executionSchema),next_cursor:z.string().optional()});
export type Execution=z.infer<typeof executionSchema>;
export type ExecutionPage=z.infer<typeof executionPageSchema>;
export type ExecutionEntry={snapshot:Execution;events:ExecutionPage['events'];sequence:number;lastSuccess:number};
export const isActive=(state:string)=>['queued','running','reconciling'].includes(state);
export function mergePage(previous:ExecutionEntry|undefined,page:ExecutionPage,now:number):ExecutionEntry{
  if(previous && page.snapshot.revision<previous.snapshot.revision) return {...previous,lastSuccess:now};
  const events=new Map(previous?.events.map(e=>[e.sequence,e]));
  for(const e of page.events)events.set(e.sequence,e);
  return {snapshot:page.snapshot,events:[...events.values()].sort((a,b)=>a.sequence-b.sequence),sequence:Math.max(previous?.sequence??0,page.next_sequence),lastSuccess:now};
}
export async function readExecution(id:string,previous:ExecutionEntry|undefined,now:()=>number=Date.now):Promise<ExecutionEntry>{
  let entry=previous;
  const generation=sessionGeneration();
  for(let pageCount=0;pageCount<100;pageCount++){
    const after=entry?.sequence??0;
    const page=await request(`/executions/${encodeURIComponent(id)}?after_sequence=${after}&limit=100`,executionPageSchema);
    if(generation!==sessionGeneration())throw new Error('SESSION_CHANGED');
    if(page.snapshot.id!==id||page.next_sequence<after||page.has_more&&page.next_sequence<=after)throw new Error('INVALID_EXECUTION_PAGE');
    entry=mergePage(entry,page,now());
    if(!page.has_more)return entry;
  }
  throw new Error('EXECUTION_PAGE_LIMIT');
}
