import { act, fireEvent, render, screen, within } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { ExecutionMap } from './ExecutionMap';
import { journeyNodes } from './useJourneyPlayback';
import { type ExecutionEntry } from '../api/executions';

const now='2026-11-14T00:00:00Z';
const completed:ExecutionEntry={snapshot:{id:'one',scenario:'access',state:'completed',current_node_id:'confirmation',revision:3,last_sequence:4,started_at:now,updated_at:now,result_code:'created',nodes:[{id:'context',kind:'context',label_code:'context',state:'succeeded'},{id:'confirmation',kind:'confirmation',label_code:'confirmation',state:'succeeded'}],edges:[],links:[],detail_complete:true},sequence:4,lastSuccess:1000,events:[
 {sequence:2,node:{id:'confirmation',kind:'confirmation',label_code:'confirmation',state:'succeeded'},state:'completed',at:now},
 {sequence:1,node:{id:'context',kind:'context',label_code:'context',state:'succeeded'},state:'running',at:now},
 {sequence:3,node:{id:'context',kind:'context',label_code:'context',state:'succeeded'},state:'completed',at:now},
 {sequence:4,node:{id:'hidden',kind:'service',label_code:'service',state:'succeeded'},state:'completed',at:now},
]};
afterEach(()=>{vi.useRealTimers();vi.unstubAllGlobals();});
describe('journey playback',()=>{
 it('uses ordered unique authorized events and reveals them without delaying the result',()=>{
  vi.useFakeTimers();expect(journeyNodes(completed)).toEqual(['context','confirmation']);
  render(<ExecutionMap entry={completed} now={1000} replay/>);
  expect(screen.getByText('Показываем пройденный путь · текущий статус указан выше')).toBeVisible();
  expect(screen.getByText('Заявка создана. Ждём согласования.')).toBeVisible();
  expect(screen.getByText('Разрешённый контекст')).toBeVisible();expect(screen.queryByText('Подтверждение')).toBeNull();
  act(()=>vi.advanceTimersByTime(1000));expect(screen.getByText('Подтверждение')).toBeVisible();
  act(()=>vi.advanceTimersByTime(1000));expect(screen.queryByText(/Показываем пройденный путь/)).toBeNull();
 });
 it('does not replay restored routes, stale states or reduced motion',()=>{
  vi.useFakeTimers();const view=render(<ExecutionMap entry={completed} now={1000}/>);
  expect(screen.getByText('Подтверждение')).toBeVisible();expect(screen.queryByText(/Показываем пройденный путь/)).toBeNull();view.unmount();
  vi.stubGlobal('matchMedia',()=>({matches:true,addEventListener:vi.fn(),removeEventListener:vi.fn()}));
  const reduced=render(<ExecutionMap entry={completed} now={1000} replay/>);expect(screen.getByText('Подтверждение')).toBeVisible();expect(screen.queryByText(/Показываем пройденный путь/)).toBeNull();reduced.unmount();
  vi.unstubAllGlobals();const stale={...completed,snapshot:{...completed.snapshot,state:'running' as const}};
  render(<ExecutionMap entry={stale} now={7000} replay/>);expect(screen.getByText('Нет свежих данных')).toBeVisible();expect(screen.getByText('Подтверждение')).toBeVisible();
 });
 it('shares playback with the dialog and stops on failure',()=>{
  vi.useFakeTimers();const view=render(<ExecutionMap entry={completed} now={1000} replay/>);
  fireEvent.click(screen.getByText('Показать маршрут'));
  const dialog=screen.getByRole('dialog');expect(within(dialog).queryByText('Подтверждение')).toBeNull();
  act(()=>vi.advanceTimersByTime(1000));expect(within(dialog).getByText('Подтверждение')).toBeVisible();
  const failed={...completed,snapshot:{...completed.snapshot,state:'failed' as const,result_code:undefined}};
  view.rerender(<ExecutionMap entry={failed} now={1000} replay/>);
  expect(screen.queryByText(/Показываем пройденный путь/)).toBeNull();expect(screen.getByRole('status')).toHaveTextContent('Не удалось завершить');
 });
 it('adds later events once and resets when selecting another route',()=>{
  vi.useFakeTimers();const initial={...completed,events:completed.events.filter(e=>e.sequence===1)};
  const view=render(<ExecutionMap key="first" entry={initial} now={1000} replay/>);
  view.rerender(<ExecutionMap key="first" entry={completed} now={1000} replay/>);
  act(()=>vi.advanceTimersByTime(1000));expect(screen.getByText('Подтверждение')).toBeVisible();
  act(()=>vi.advanceTimersByTime(1000));expect(screen.queryByText(/Показываем пройденный путь/)).toBeNull();
  view.rerender(<ExecutionMap key="second" entry={{...completed,snapshot:{...completed.snapshot,id:'second'}}} now={1000}/>);
  expect(screen.queryByText(/Показываем пройденный путь/)).toBeNull();expect(screen.getByText('Подтверждение')).toBeVisible();
 });
 it('preserves each transfer branch and shares its replay with the detailed view',()=>{
  vi.useFakeTimers();
  const first={...completed.snapshot,id:'first',scenario:'time_off' as const};
  const second={...completed.snapshot,id:'second',scenario:'time_off' as const};
  const parent={...completed,snapshot:{...completed.snapshot,id:'transfer',scenario:'transfer' as const,children:[first,second]}};
  const entries={first:{...completed,snapshot:first},second:{...completed,snapshot:second}};
  render(<ExecutionMap entry={parent} entries={entries} now={1000} replay/>);
  expect(within(screen.getByRole('region',{name:'Операция переноса 1'})).getByText('Разрешённый контекст')).toBeVisible();
  expect(within(screen.getByRole('region',{name:'Операция переноса 2'})).queryByText('Разрешённый контекст')).toBeNull();
  fireEvent.click(screen.getByText('Показать маршрут'));
  act(()=>vi.advanceTimersByTime(1000));act(()=>vi.advanceTimersByTime(1000));
  const detail=screen.getByRole('dialog');expect(within(within(detail).getByRole('region',{name:'Операция переноса 2'})).getByText('Разрешённый контекст')).toBeVisible();
 });
});
