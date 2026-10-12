import { vi } from 'vitest';
import { handle, resetMock } from '../../mock/server';

export interface Call {
  method: string;
  url: string;
  body: string;
  headers: Record<string, string>;
}

// Routes fetch() to the in-process fixture gateway (web/mock) and records
// every call, so component tests exercise real request shapes.
export function installApi(opts: { setup?: boolean; login?: boolean } = {}): Call[] {
  resetMock(opts);
  const calls: Call[] = [];
  vi.stubGlobal('fetch', async (input: RequestInfo | URL, init: RequestInit = {}) => {
    const url = typeof input === 'string' ? input : input instanceof URL ? input.pathname + input.search : input.url;
    const method = init.method ?? 'GET';
    const body = typeof init.body === 'string' ? init.body : init.body instanceof FormData ? 'filename="upload.bin"' : '';
    const headers = Object.fromEntries(new Headers(init.headers).entries());
    calls.push({ method, url, body, headers });
    const r = await handle(method, url, body, headers);
    return new Response(r.status === 204 || r.body === '' ? null : r.body, { status: r.status, headers: r.headers });
  });
  return calls;
}
