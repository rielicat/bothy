// Mock gateway for UI development: `npm run mock` serves the SPA with every
// /api route answered from in-memory fixtures and a scripted SSE run, so the
// screens can be built and reviewed without a Go build or real providers.
//
//   MOCK_SETUP=1  start in first-run state (setup token "demo-setup-token")
//   MOCK_LOGIN=0  start logged out (any username, any password but "wrong")
//   MOCK=a,b      scenarios on by default (see domains/core.ts; ?mock= per page)
//
// The routes and fixtures live in domains/, one file per area of the UI, so
// each UI chunk edits only its own (docs/design/ui.md §18.3 F1).
//
// Dev only: vite.config.ts installs it only when BOTHY_MOCK=1 (`npm run mock`,
// or `npm run preview:mock` for the built app under the production CSP), so
// nothing here ships in the build.
import type { Plugin } from 'vite';
import './domains/shell.ts';
import './domains/setup.ts';
import './domains/chat.ts';
import './domains/approvals.ts';
import './domains/knowledge.ts';
import './domains/activity.ts';
import './domains/settings.ts';
import './domains/connections.ts';
import './domains/security.ts';
import './domains/inbox.ts';
import { apiDown, core, fail, MOCK_COOKIE, open, resetAll, routes, sanitize, sleep, slow, type Req, type Res } from './domains/core.ts';

resetAll();

// Unit tests reuse the fixture gateway through handle(); resetMock gives
// each test a clean state, optionally with scenarios on (`mock: 'paused'`).
export function resetMock(opts: { setup?: boolean; login?: boolean; mock?: string } = {}) {
  resetAll({ setup: opts.setup ?? false, login: opts.login ?? true, mock: opts.mock ?? '' });
}

let latency = 80;

// A page opened with ?mock= starts from fresh fixtures, in setup or signed
// out when the list asks for it, and the list rides along in a cookie.
function applyScenarios(res: Res, url: URL) {
  const list = sanitize(url.searchParams.get('mock') ?? '');
  const names = new Set(list.split(',').map((s) => s.split(':')[0]));
  res.setHeader('Set-Cookie', list ? `${MOCK_COOKIE}=${encodeURIComponent(list)}; Path=/; SameSite=Lax` : `${MOCK_COOKIE}=; Path=/; Max-Age=0; SameSite=Lax`);
  resetAll({ setup: names.has('setup'), login: !names.has('logged-out') });
}

async function dispatch(req: Req, res: Res, next: () => void) {
  const url = new URL(req.url ?? '/', 'http://mock');
  if (!url.pathname.startsWith('/api/')) {
    if (url.searchParams.has('mock')) applyScenarios(res, url);
    return next();
  }
  const route = routes.find(([m, re]) => m === req.method && re.test(url.pathname));
  if (!route) return fail(res, 404, 'not_found', `mock: no route for ${req.method} ${url.pathname}`);
  const always = url.pathname === '/api/mock/scenarios';
  if (apiDown(req) && !always) return fail(res, 503, 'unavailable', 'mock: api-down');
  if (!open.has(url.pathname) && !core.loggedIn) return fail(res, 401, 'unauthorized', 'Log in first.');
  if (req.method !== 'GET' && !open.has(url.pathname) && req.headers['x-csrf-token'] !== 'mock-csrf') return fail(res, 403, 'forbidden', 'missing or invalid X-CSRF-Token', 'GET /api/auth/me returns the token');
  if (slow(req) && !always) await sleep(1500);
  else if (latency) await sleep(latency);
  try {
    await route[2](req, res, url.pathname.match(route[1])!, url.searchParams);
  } catch (e) {
    fail(res, 500, 'internal', `mock: ${String(e)}`);
  }
}

// In-process entry point for unit tests: no latency, no SSE.
export async function handle(method: string, url: string, body = '', headers: Record<string, string> = {}): Promise<{ status: number; headers: Record<string, string>; body: string }> {
  latency = 0;
  const out = { status: 200, headers: {} as Record<string, string>, body: '' };
  const listeners: Record<string, ((c?: { toString(): string }) => void)[]> = {};
  const req: Req = { method, url, headers: Object.fromEntries(Object.entries(headers).map(([k, v]) => [k.toLowerCase(), v])), on: (ev, fn) => void (listeners[ev] ??= []).push(fn) };
  let done!: () => void;
  const finished = new Promise<void>((r) => (done = r));
  const res: Res = {
    get statusCode() {
      return out.status;
    },
    set statusCode(v: number) {
      out.status = v;
    },
    setHeader: (k, v) => void (out.headers[k] = v),
    write: (s) => void (out.body += s),
    end: (s) => {
      out.body += s ?? '';
      done();
    },
    on: () => {},
  };
  const run = dispatch(req, res, () => {
    out.status = 404;
    done();
  });
  queueMicrotask(() => {
    if (body) listeners.data?.forEach((fn) => fn({ toString: () => body }));
    listeners.end?.forEach((fn) => fn());
  });
  await Promise.race([finished, run.then(() => finished)]);
  return out;
}

// SPEC §8.1 security headers, applied by `vite preview` so the production
// build is exercised under the real CSP (the dev server injects inline styles).
const CSP =
  "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; font-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'";

export function mockGateway(): Plugin {
  // node:http types are not installed; the structural Req/Res cover what the routes use.
  const mount = (use: (fn: (req: unknown, res: unknown, next: () => void) => void) => void, headers: boolean) => {
    use((req, res, next) => {
      if (headers) {
        const r = res as Res;
        r.setHeader('Content-Security-Policy', CSP);
        r.setHeader('X-Content-Type-Options', 'nosniff');
        r.setHeader('Referrer-Policy', 'no-referrer');
      }
      void dispatch(req as Req, res as Res, next);
    });
  };
  // Both hooks must return nothing: Vite runs a returned function as a post hook.
  return {
    name: 'bothy-mock-gateway',
    configureServer(server) {
      mount((fn) => void server.middlewares.use(fn), false);
    },
    configurePreviewServer(server) {
      mount((fn) => void server.middlewares.use(fn), true);
    },
  };
}
