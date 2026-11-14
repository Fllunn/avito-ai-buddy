import { afterEach, describe, expect, it, vi } from 'vitest';
import { apiBaseURL, documentURL } from './urls';

afterEach(() => vi.unstubAllEnvs());
describe('deployment URLs', () => {
  it('keeps root deployment addresses', () => {
    vi.stubEnv('BASE_URL', '/'); vi.stubEnv('VITE_API_BASE_URL', '');
    expect(apiBaseURL()).toBe('');
    expect(documentURL('/demo/docs/tracker')).toBe('/demo/docs/tracker');
  });
  it('prefixes the API and internal documents', () => {
    vi.stubEnv('BASE_URL', '/avito_buddy/'); vi.stubEnv('VITE_API_BASE_URL', '');
    expect(apiBaseURL()).toBe('/avito_buddy');
    expect(documentURL('/demo/docs/tracker')).toBe('/avito_buddy/demo/docs/tracker');
    expect(documentURL('https://example.test/docs')).toBe('https://example.test/docs');
  });
  it('supports a separate API origin without changing document URLs', () => {
    vi.stubEnv('BASE_URL', '/avito_buddy/'); vi.stubEnv('VITE_API_BASE_URL', 'https://api.example.test/');
    expect(apiBaseURL()).toBe('https://api.example.test');
    expect(documentURL('/demo/docs/tracker')).toBe('/avito_buddy/demo/docs/tracker');
  });
});
