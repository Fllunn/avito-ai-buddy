import { act,render,screen,waitFor } from '@testing-library/react';
import { QueryClient,QueryClientProvider,focusManager } from '@tanstack/react-query';
import { afterAll,afterEach,beforeAll,beforeEach,describe,it,expect,vi } from 'vitest';
import { http,HttpResponse } from 'msw';
import { setupServer } from 'msw/node';
import { resetSession,sessionGeneration } from '../api/client';
import { useExecutions } from './useExecutions';
const now='2026-11-14T00:00:00Z';
let reads=0,lists=0,finished=false,broken=false,human=false;
const snapshot=()=>({id:'route',scenario:'orientation',state:human?'waiting_user':finished?'completed':'running',current_node_id:'answer',revision:finished?2:1,last_sequence:0,started_at:now,updated_at:now,nodes:[],edges:[],links:[],detail_complete:true});
const server=setupServer(http.get('*/api/executions',()=>{lists++;return HttpResponse.json({items:[snapshot()]});}),http.get('*/api/executions/route',()=>{reads++;return broken?new HttpResponse(null,{status:503}):HttpResponse.json({snapshot:snapshot(),events:[],next_sequence:0,has_more:false,as_of:now,detail_complete:true});}));
function View({epoch}:{epoch:number}){const q=useExecutions(epoch,true,false);return <><p>{q.entries.route?.snapshot.state??'empty'}</p><p>{q.unavailable?'unavailable':'available'}</p><span>{Object.keys(q.entries).length}</span></>;}
function fixture(){const client=new QueryClient({defaultOptions:{queries:{retry:false,gcTime:0}}});const epoch=sessionGeneration();const v=render(<QueryClientProvider client={client}><View epoch={epoch}/></QueryClientProvider>);return {...v,client};}
beforeAll(()=>{server.listen({onUnhandledFrame:'error'});vi.stubEnv('VITE_API_BASE_URL','http://localhost');});afterAll(()=>{server.close();vi.unstubAllEnvs();});beforeEach(()=>{resetSession();reads=0;lists=0;finished=false;broken=false;human=false;focusManager.setFocused(true);});afterEach(()=>server.resetHandlers());
describe('single execution coordinator',()=>{
 it('deduplicates discovery, polls active work and stops at the final snapshot',async()=>{fixture();await screen.findByText('running');expect(reads).toBe(1);expect(lists).toBe(2);finished=true;await screen.findByText('completed',{}, {timeout:3000});const count=reads;await act(()=>new Promise(r=>setTimeout(r,1150)));expect(reads).toBe(count);});
 it('stops at human waiting and refreshes on focus',async()=>{human=true;fixture();await screen.findByText('waiting_user');const count=reads;await act(()=>new Promise(r=>setTimeout(r,1100)));expect(reads).toBe(count);act(()=>focusManager.setFocused(false));act(()=>focusManager.setFocused(true));await waitFor(()=>expect(reads).toBeGreaterThan(count));});
 it('keeps the last snapshot through a map error',async()=>{const f=fixture();await screen.findByText('running');broken=true;await act(async()=>{await f.client.invalidateQueries({queryKey:['executions']});});expect(screen.getByText('running')).toBeVisible();expect(await screen.findByText('unavailable')).toBeVisible();});
 it('clears entries on a new session and ignores old responses',async()=>{const f=fixture();await screen.findByText('running');server.use(http.get('*/api/executions',()=>HttpResponse.json({items:[]})));resetSession();f.rerender(<QueryClientProvider client={f.client}><View epoch={sessionGeneration()}/></QueryClientProvider>);await screen.findByText('empty');expect(screen.queryByText('running')).toBeNull();});
});

describe('restoring linked executions',()=>{
 it('restores an old logical request referenced by a recent continuation',async()=>{
  finished=true;
  server.use(http.get('*/api/executions/route',()=>{reads++;return HttpResponse.json({snapshot:{...snapshot(),links:[{kind:'parent',id:'original'}]},events:[],next_sequence:0,has_more:false,as_of:now,detail_complete:true});}),http.get('*/api/executions/original',()=>HttpResponse.json({snapshot:{...snapshot(),id:'original',state:'waiting_user'},events:[],next_sequence:0,has_more:false,as_of:now,detail_complete:true})));
  fixture();await screen.findByText('completed');await screen.findByText('2');
 });
 it('drops an in-flight response from a previous identity',async()=>{
  let release:()=>void=()=>{};
  const gate=new Promise<void>(resolve=>{release=resolve;});
  server.use(http.get('*/api/executions/route',async()=>{await gate;return HttpResponse.json({snapshot:snapshot(),events:[],next_sequence:0,has_more:false,as_of:now,detail_complete:true});}));
  const f=fixture();await waitFor(()=>expect(lists).toBeGreaterThan(0));
  server.use(http.get('*/api/executions',()=>HttpResponse.json({items:[]})));
  resetSession();f.rerender(<QueryClientProvider client={f.client}><View epoch={sessionGeneration()}/></QueryClientProvider>);
  await act(async()=>{release();await gate;});await screen.findByText('empty');expect(screen.queryByText('running')).toBeNull();
 });
});
