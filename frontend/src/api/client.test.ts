import { afterEach, describe, expect, it, vi } from 'vitest';
import { z } from 'zod';
import { ApiError, errorText, request, resetSession } from './client';
afterEach(() => { vi.unstubAllGlobals(); resetSession(); });
describe('Session generation and DTO boundary', () => {
 it('ignores an old response even when the transport ignores cancellation', async () => {
  let release!: (response: Response) => void;
  vi.stubGlobal('fetch',vi.fn(()=>new Promise<Response>((resolve)=>{release=resolve;})));
  const pending=request('/me',z.object({secret:z.string()}));
  resetSession();release(new Response(JSON.stringify({secret:'old synthetic profile'}),{status:200}));
  await expect(pending).rejects.toMatchObject({code:'SESSION_CHANGED'});
 });
 it('rejects a malformed DTO and hides unknown error text', async () => {
  vi.stubGlobal('fetch',vi.fn(async()=>new Response(JSON.stringify({version:'wrong'}),{status:200})));
  await expect(request('/card',z.object({version:z.number().int().positive()}))).rejects.toMatchObject({code:'INVALID_RESPONSE'});
  expect(errorText(new ApiError('synthetic-private-body',500))).not.toContain('synthetic-private-body');
 });
 it('accepts an empty logout and returns every safe calendar violation', async () => {
  vi.stubGlobal('fetch',vi.fn(async()=>new Response(null,{status:204})));
  await expect(request('/session',z.undefined(),'DELETE')).resolves.toBeUndefined();
  expect(errorText(new ApiError('VALIDATION_ERROR',400,{notice:'Не раньше следующего рабочего дня.',end_workday:'Конец должен быть рабочим.'}))).toContain('Конец должен быть рабочим.');
 });
});
