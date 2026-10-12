import type { Error as ErrorEnvelope } from './types.gen';

export class ApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
    readonly fix?: string,
  ) {
    super(message);
    this.name = 'ApiError';
  }
}

let csrf = '';

// Set from dto.Me.csrf (or dto.SetupClaimResp.csrf during setup).
export function setCsrf(token: string) {
  csrf = token;
}

const unsafe = new Set(['POST', 'PUT', 'PATCH', 'DELETE']);

export async function request<T>(method: string, path: string, body?: unknown, headers: HeadersInit = {}): Promise<T> {
  const h = new Headers(headers);
  if (body !== undefined && !(body instanceof FormData)) h.set('Content-Type', 'application/json');
  if (unsafe.has(method) && csrf) h.set('X-CSRF-Token', csrf);
  const res = await fetch(path, {
    method,
    headers: h,
    credentials: 'same-origin',
    body: body === undefined ? undefined : body instanceof FormData ? body : JSON.stringify(body),
  });
  if (!res.ok) {
    let env: ErrorEnvelope | undefined;
    try {
      env = (await res.json()) as ErrorEnvelope;
    } catch {
      // non-JSON error bodies (proxies, 421 pages) fall through to the generic error
    }
    const e = env?.error;
    throw new ApiError(res.status, e?.code ?? 'internal', e?.message ?? res.statusText, e?.fix);
  }
  if (res.status === 204 || res.headers.get('Content-Length') === '0') return undefined as T;
  return (await res.json()) as T;
}

export const api = {
  get: <T>(path: string) => request<T>('GET', path),
  post: <T>(path: string, body?: unknown) => request<T>('POST', path, body),
  put: <T>(path: string, body?: unknown, headers?: HeadersInit) => request<T>('PUT', path, body, headers),
  patch: <T>(path: string, body?: unknown) => request<T>('PATCH', path, body),
  del: <T = void>(path: string) => request<T>('DELETE', path),
};
