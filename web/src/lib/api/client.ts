import type { Error as ErrorEnvelope, FieldError } from './types.gen';

export class ApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
    readonly fix?: string,
    readonly fields: FieldError[] = [],
  ) {
    super(message);
    this.name = 'ApiError';
  }
}

let csrf = '';
let unauthorized: (() => void) | null = null;

// Set from dto.Me.csrf (or dto.SetupClaimResp.csrf during setup).
export function setCsrf(token: string) {
  csrf = token;
}

// Called once per 401 from a request that was sent with a session; the app
// uses it to drop the session and route to /login.
export function onUnauthorized(fn: () => void) {
  unauthorized = fn;
}

const unsafe = new Set(['POST', 'PUT', 'PATCH', 'DELETE']);

// PATCH /api/config answers 400 with config.FieldError objects; accept them
// at the top level, under "fields"/"errors", or inside the error envelope.
function fieldErrors(body: unknown): FieldError[] {
  const pick = (v: unknown): FieldError[] =>
    Array.isArray(v) ? v.filter((x): x is FieldError => !!x && typeof x === 'object' && 'path' in x && 'message' in x) : [];
  if (Array.isArray(body)) return pick(body);
  if (body && typeof body === 'object') {
    const b = body as Record<string, unknown>;
    const inner = (b.error && typeof b.error === 'object' ? b.error : {}) as Record<string, unknown>;
    return [b.fields, b.errors, inner.fields, inner.errors].map(pick).find((l) => l.length) ?? [];
  }
  return [];
}

export async function raw(method: string, path: string, body?: unknown, headers: HeadersInit = {}): Promise<Response> {
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
    let parsed: unknown;
    try {
      parsed = await res.json();
    } catch {
      // non-JSON error bodies (proxies, 421 pages) fall through to the generic error
    }
    const e = (parsed as ErrorEnvelope | undefined)?.error;
    const fields = fieldErrors(parsed);
    // A 401 from these means a wrong credential in the request, not a dead session.
    if (res.status === 401 && unauthorized && !/^\/api\/(auth\/login|auth\/password|setup\/)/.test(path)) {
      unauthorized();
    }
    const message = e?.message ?? (fields.length ? fields.map((f) => f.message).join('; ') : res.statusText || `HTTP ${res.status}`);
    throw new ApiError(res.status, e?.code ?? (res.status === 401 ? 'unauthorized' : 'internal'), message, e?.fix, fields);
  }
  return res;
}

export async function request<T>(method: string, path: string, body?: unknown, headers: HeadersInit = {}): Promise<T> {
  const res = await raw(method, path, body, headers);
  if (res.status === 204 || res.status === 202 || res.headers.get('Content-Length') === '0') {
    const text = res.status === 204 ? '' : await res.text();
    return (text ? JSON.parse(text) : undefined) as T;
  }
  return (await res.json()) as T;
}

export function qs(params: Record<string, string | number | boolean | undefined | null>): string {
  const p = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) if (v !== undefined && v !== null && v !== '' && v !== false) p.set(k, String(v));
  const s = p.toString();
  return s ? `?${s}` : '';
}

export const api = {
  get: <T>(path: string) => request<T>('GET', path),
  text: async (path: string) => (await raw('GET', path)).text(),
  post: <T>(path: string, body?: unknown) => request<T>('POST', path, body),
  put: <T>(path: string, body?: unknown, headers?: HeadersInit) => request<T>('PUT', path, body, headers),
  patch: <T>(path: string, body?: unknown) => request<T>('PATCH', path, body),
  del: <T = void>(path: string) => request<T>('DELETE', path),
};

export function errorText(e: unknown): string {
  if (e instanceof ApiError) return e.fix ? `${e.message} ${e.fix}` : e.message;
  if (e instanceof Error) return e.message;
  return String(e);
}
