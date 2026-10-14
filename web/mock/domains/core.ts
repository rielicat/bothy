// Shared plumbing for the fixture gateway: request helpers, the route table,
// the event stream, the people every domain refers to, the signed-in state and
// scenario switches. Each file next to this one owns one area of the API
// (docs/design/ui.md §18.3 F1), so a UI chunk edits only its own domain.
import type { BannerData, Principal, SetupState, User } from '../../src/lib/api/types.gen.ts';

export interface Req {
  method?: string;
  url?: string;
  headers: Record<string, string | string[] | undefined>;
  on(ev: 'data' | 'end' | 'close', fn: (chunk?: { toString(enc?: string): string }) => void): void;
}
export interface Res {
  statusCode: number;
  setHeader(k: string, v: string): void;
  write(s: string): void;
  end(s?: string): void;
  on(ev: 'close', fn: () => void): void;
}
declare const process: { env: Record<string, string | undefined> };

export const env = process.env;
export const clone = <T>(v: T): T => structuredClone(v);
export const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));
const alphabet = '0123456789abcdefghjkmnpqrstvwxyz';
export const newId = (p: string) => `${p}_` + Array.from({ length: 26 }, () => alphabet[Math.floor(Math.random() * 32)]).join('');
export const nowISO = () => new Date().toISOString();

export const ZERO = '0001-01-01T00:00:00Z';
const now = Date.now();
export const ago = (min: number) => new Date(now - min * 60_000).toISOString();
export const ahead = (min: number) => new Date(now + min * 60_000).toISOString();

// Ids that several domains point at.
export const ids = {
  trip: 'ses_01jtrip000000000000000000',
  tripRun: 'run_01jtrip1000000000000000',
  brief: 'ses_01jbrief00000000000000000',
  briefRun: 'run_01jbrief1000000000000000',
  briefSchedule: 'sch_01jbrief00000000000000000',
};

export const owner: User = {
  id: 'usr_01jsam0000000000000000000',
  username: 'sam',
  display_name: 'Sam',
  role: 'owner',
  timezone: 'Europe/London',
  disabled: false,
  created_at: ago(60 * 24 * 30),
  updated_at: ago(60 * 24 * 2),
  last_login_at: ago(2),
};

export const member: User = {
  id: 'usr_01jalex000000000000000000',
  username: 'alex',
  display_name: 'Alex',
  role: 'member',
  timezone: 'Europe/London',
  disabled: false,
  created_at: ago(60 * 24 * 12),
  updated_at: ago(60 * 24 * 12),
  last_login_at: ago(60 * 5),
};

export const principals: Principal[] = [
  { id: owner.id, kind: 'user', user_id: owner.id, display_name: 'Sam', workspace_id: 'ws_01jsamws000000000000000000', tool_profile: 'standard', created_at: owner.created_at },
  { id: 'spc_01jfamily0000000000000000', kind: 'space', user_id: owner.id, display_name: 'Ops team', workspace_id: 'ws_01jfamws000000000000000000', tool_profile: 'restricted', created_at: ago(60 * 24 * 9) },
];

export const SETUP_TOKEN = 'demo-setup-token';
export const boot = newId('boot').slice(5, 13);

export interface ResetOpts {
  setup?: boolean;
  login?: boolean;
  /** Scenario list, as in ?mock= (comma separated, `name` or `name:arg`). */
  mock?: string;
}

// Who is signed in, setup progress, pause and banners: read by every domain.
export const core = {
  setup: {} as SetupState,
  loggedIn: true,
  me: clone(owner),
  mode: 'single',
  paused: false,
  // Who paused and when, while paused from the UI.
  pausedBy: null as User | null,
  pausedAt: '',
  banners: [] as BannerData[],
  // Never reset: open event streams would take a lower id for a replay.
  seq: 0,
};

const resets: ((opts: ResetOpts) => void)[] = [];

/** Registers a domain's seeding; server.ts runs every one at start-up and on each reset. */
export function onReset(fn: (opts: ResetOpts) => void) {
  resets.push(fn);
}

export function resetAll(opts: ResetOpts = {}) {
  defaults = parse(opts.mock ?? env.MOCK ?? '');
  const setup = opts.setup ?? (defaults.has('setup') || env.MOCK_SETUP === '1');
  const login = opts.login ?? !(defaults.has('logged-out') || env.MOCK_LOGIN === '0');
  Object.assign(core, {
    setup: { needs_setup: setup, claimed: false, owner_set: !setup, sandbox_available: true },
    loggedIn: !setup && login,
    me: clone(owner),
    mode: 'single',
    paused: false,
    pausedBy: null,
    pausedAt: '',
    banners: [{ level: 'warn', code: 'mcp_pins', text: 'One MCP tool changed and waits for your review.', action: '/settings/mcp' }],
  });
  for (const fn of resets) fn({ ...opts, setup, login });
}

// Event stream (SPEC §8.4): every connected tab gets every event.
export const clients = new Set<Res>();

export function emit(type: string, data: unknown, sessionId?: string, runId?: string) {
  const ev = { seq: ++core.seq, type, principal_id: owner.id, session_id: sessionId, run_id: runId, at: nowISO(), data };
  const frame = `id: ${boot}-${ev.seq}\nevent: ${type}\ndata: ${JSON.stringify(ev)}\n\n`;
  for (const c of clients) c.write(frame);
}

export function json(res: Res, status: number, body?: unknown) {
  res.statusCode = status;
  res.setHeader('Content-Type', 'application/json');
  res.setHeader('Cache-Control', 'no-store');
  res.end(body === undefined ? '' : JSON.stringify(body));
}

export function fail(res: Res, status: number, code: string, message: string, fix?: string) {
  json(res, status, { error: { code, message, fix } });
}

export function body(req: Req): Promise<string> {
  return new Promise((resolve) => {
    let b = '';
    req.on('data', (c) => (b += c?.toString('latin1') ?? ''));
    req.on('end', () => resolve(b));
  });
}

export type Handler = (req: Req, res: Res, m: RegExpMatchArray, q: URLSearchParams) => unknown | Promise<unknown>;
export const routes: [string, RegExp, Handler][] = [];
/** Paths that answer without a session (setup and sign-in). */
export const open = new Set<string>();

/** Adds a route; `{id}` matches one segment, `{path...}` the rest. */
export function on(method: string, path: string, h: Handler, opts: { open?: boolean } = {}) {
  if (opts.open) open.add(path);
  routes.push([method, new RegExp(`^${path.replace(/\{(\w+)\.\.\.\}/g, '(.+)').replace(/\{(\w+)\}/g, '([^/]+)')}$`), h]);
}

// Scenarios: named switches for states the default fixtures don't show
// (banners, failures, detection results). Opening any page with
// ?mock=a,b:arg resets the fixtures and stores the list in a cookie, which
// later /api calls carry; ?mock= alone resets and clears it. MOCK=a,b in the
// environment or resetMock({ mock }) set a default list.
export const MOCK_COOKIE = 'bothy_mock';
const known = new Map<string, { about: string; domain: string }>();
let defaults = new Map<string, string>();

function parse(list: string): Map<string, string> {
  const out = new Map<string, string>();
  for (const item of list.split(',')) {
    const [name, ...arg] = item.trim().split(':');
    if (name) out.set(name, arg.join(':'));
  }
  return out;
}

export function sanitize(list: string): string {
  return list.toLowerCase().replace(/[^a-z0-9_:,.-]/g, '');
}

function cookie(req: Req, name: string): string {
  const raw = req.headers.cookie;
  for (const part of (Array.isArray(raw) ? raw.join(';') : (raw ?? '')).split(';')) {
    const [k, ...v] = part.trim().split('=');
    if (k === name) return decodeURIComponent(v.join('='));
  }
  return '';
}

/** Every scenario active for this request, name → argument ('' without one). */
export function active(req?: Req): Map<string, string> {
  const out = new Map(defaults);
  if (req) for (const [k, v] of parse(sanitize(cookie(req, MOCK_COOKIE)))) out.set(k, v);
  return out;
}

export interface Scenario {
  (req?: Req): boolean;
  /** The text after `name:` in the list, or undefined when the scenario is off. */
  arg(req?: Req): string | undefined;
}

/** Declares a scenario (listed at /api/mock/scenarios) and returns its test. */
export function scenario(name: string, about: string, domain = 'core'): Scenario {
  if (!known.has(name)) known.set(name, { about, domain });
  return Object.assign((req?: Req) => active(req).has(name), { arg: (req?: Req) => active(req).get(name) });
}

export function scenarios() {
  return [...known].map(([name, s]) => ({ name, ...s })).sort((a, b) => a.domain.localeCompare(b.domain) || a.name.localeCompare(b.name));
}

export const setupMode = scenario('setup', 'Start at first-run setup (setup code demo-setup-token). Applied when the page loads.');
export const loggedOut = scenario('logged-out', "Start signed out (any username, any password but 'wrong'). Applied when the page loads.");
export const asMember = scenario('member', 'Signed in as Alex, a member, instead of Sam, the owner.');
export const pausedNow = scenario('paused', 'An admin paused Bothy: no new work starts.');
export const slow = scenario('slow', 'Every /api call takes 1.5 s, to show loading states.');
export const apiDown = scenario('api-down', 'Every /api call fails with 503, as when the server restarts.');

export const viewer = (req?: Req): User => (asMember(req) ? clone(member) : core.me);
export const isPaused = (req?: Req) => core.paused || pausedNow(req);

/** Who paused and when, as Me and the paused banner say: the paused scenario was Sam, 42 minutes ago. */
export function pauseInfo(req?: Req): { by: User; at: string } | null {
  if (core.paused && core.pausedBy) return { by: core.pausedBy, at: core.pausedAt };
  return pausedNow(req) ? { by: owner, at: ago(42) } : null;
}

export function pausedBanner(req?: Req): BannerData {
  const p = pauseInfo(req);
  return { level: 'error', code: 'paused', text: `Bothy is paused by ${p?.by.display_name ?? 'an admin'}.`, action: '/settings/security', by: p?.by.id, by_name: p?.by.display_name, at: p?.at };
}
