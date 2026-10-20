// Sign in with Google and GitHub (SPEC §15): the sign-in page's buttons and
// the provider round trip, Account → Sign-in methods, and Security → Sign-in.
// The provider is faked by /api/mock/oauth/{p}, which sends the browser
// straight back to the callback as if the person had agreed.
import type { SignIn, SignInAdmin, SignInOption, SignInProvider, SignInProviderStatus } from '../../src/lib/api/types.gen.ts';
import { ago, body, core, fail, json, nowISO, on, onReset, owner, scenario, ZERO, type Req, type Res } from './core.ts';
import { sealed, state as security } from './security.ts';
import { state as settings, writeConfig } from './settings.ts';

const names: Record<SignInProvider, string> = { google: 'Google', github: 'GitHub' };
const order: SignInProvider[] = ['google', 'github'];

const googleOn = scenario('signin-google', 'Google sign-in is set up and on (sign-in page and Security).', 'signin');
const githubOn = scenario('signin-github', 'GitHub sign-in is set up and on.', 'signin');
const failWith = scenario('signin-fail', 'The provider round trip comes back refused: signin-fail:<code> (default not_allowed).', 'signin');
const linkedGoogle = scenario('signin-linked', 'Sam has connected Google as sam@acme.com (turns Google on).', 'signin');
const onlyProvider = scenario('signin-only', 'The account has no password and signs in with Google only.', 'signin');
const noAddress = scenario('signin-no-address', "Bothy's web address isn't set, so sign-in can't be turned on.", 'signin');
const httpAddress = scenario('signin-http-address', "Bothy's web address starts with http://.", 'signin');
const checkFail = scenario('signin-check-fail', 'Checking the client ID and secret fails: signin-check-fail:<code> (default key_rejected).', 'signin');
const noSecret = scenario('signin-no-secret', "Google is on but its saved client secret is missing (turns Google on).", 'signin');
const wrongAddress = scenario('wrong-address', 'Starting a provider sign-in answers that Bothy lives at https://bothy.acme.com.', 'signin');
const teamMode = scenario('team-mode', 'Team mode is on, so sign-in can add people.', 'signin');

interface Provider {
  enabled: boolean;
  clientId: string;
  secret: boolean;
  orgs: string[];
}

const blank = (): Provider => ({ enabled: false, clientId: '', secret: false, orgs: [] });

export const state = {
  allow: [] as string[],
  allowNew: false,
  providers: { google: blank(), github: blank() } as Record<SignInProvider, Provider>,
  linked: [] as SignIn[],
  unlinked: [] as SignInProvider[],
  seeded: '',
  // The round trip in progress: what the mock provider's answer finishes.
  flow: null as { p: SignInProvider; intent: 'signin' | 'link'; next: string } | null,
};

onReset(() => Object.assign(state, { allow: [], allowNew: false, providers: { google: blank(), github: blank() }, linked: [], unlinked: [], seeded: '', flow: null }));

// Scenarios set providers up once per scenario list, so later PATCHes stick.
// Keyed by the list rather than once per reset: a late request from the
// previous page, with its own list, must not seed the next page's state.
function seed(req?: Req) {
  const key = [googleOn, githubOn, linkedGoogle, onlyProvider, noSecret].map((s) => (s(req) ? '1' : '0')).join('');
  if (state.seeded === key) return;
  state.seeded = key;
  state.providers = { google: blank(), github: blank() };
  state.allow = [];
  const set = (p: SignInProvider, clientId: string) => Object.assign(state.providers[p], { enabled: true, clientId, secret: true });
  if (googleOn(req) || linkedGoogle(req) || onlyProvider(req) || noSecret(req)) set('google', '1234-mock.apps.googleusercontent.com');
  if (githubOn(req)) Object.assign(set('github', 'Ov23liMock'), { orgs: ['acme-inc'] });
  if (noSecret(req)) state.providers.google.secret = false;
  if (state.providers.google.enabled || state.providers.github.enabled) state.allow = ['acme.com'];
}

function publicURL(req?: Req): string {
  if (noAddress(req)) return '';
  if (httpAddress(req)) return 'http://bothy.acme.com';
  const v = settings.effective.entries.find((e) => e.path === 'server.public_url')?.value;
  // The config fixture has no web address; the mock pretends to run at one.
  return typeof v === 'string' && v ? v : 'https://bothy.acme.com';
}

function address(url: string): 'ok' | 'missing' | 'not_https' {
  if (!url) return 'missing';
  return url.startsWith('https://') || /^http:\/\/(localhost|127\.0\.0\.1)(:|\/|$)/.test(url) ? 'ok' : 'not_https';
}

// GitHub matches exact addresses only; a domain lets in Google Workspace accounts.
const hasRules = (p: SignInProvider, allow: string[], orgs: string[]) => (p === 'github' ? allow.some((e) => e.includes('@')) || orgs.length > 0 : allow.length > 0);

function status(req: Req | undefined, p: SignInProvider): SignInProviderStatus {
  seed(req);
  const c = state.providers[p];
  const url = publicURL(req);
  const rules = hasRules(p, state.allow, c.orgs);
  let problem = '';
  if (address(url) !== 'ok') problem = 'no_address';
  else if (!c.clientId || !rules || (!c.secret && !noSecret(req))) problem = 'incomplete';
  else if (!c.secret) problem = 'no_secret';
  else if (sealed(req)) problem = 'sealed';
  return {
    provider: p,
    name: names[p],
    enabled: c.enabled,
    ready: c.enabled && !problem,
    problem: problem || undefined,
    redirect_uri: address(url) === 'ok' ? `${new URL(url).origin}/api/auth/oauth/${p}/callback` : '',
    client_id: c.clientId,
    secret_saved: c.secret,
    secret_source: c.secret ? 'vault' : undefined,
    orgs: [...c.orgs],
  };
}

const offered = (req?: Req): SignInOption[] => order.filter((p) => status(req, p).ready).map((p) => ({ provider: p, name: names[p] }));

function linked(req?: Req): SignIn[] {
  const out = [...state.linked];
  if ((linkedGoogle(req) || onlyProvider(req)) && !state.unlinked.includes('google') && !out.some((l) => l.provider === 'google'))
    out.unshift({ provider: 'google', user_id: owner.id, email: 'sam@acme.com', created_at: ago(60 * 24 * 6), last_used_at: ago(60 * 2) });
  return out.sort((a, b) => a.provider.localeCompare(b.provider));
}

function admin(req: Req): SignInAdmin {
  seed(req);
  const url = publicURL(req);
  const mode = settings.effective.entries.find((e) => e.path === 'mode')?.value;
  return { public_url: url, address: address(url), team_mode: mode === 'multi' || teamMode(req), allow: [...state.allow], allow_new: state.allowNew, providers: order.map((p) => status(req, p)) };
}

function provider(res: Res, raw: string): SignInProvider | null {
  if (raw === 'google' || raw === 'github') return raw;
  fail(res, 404, 'not_found', 'not found');
  return null;
}

function redirect(res: Res, status: number, to: string) {
  res.statusCode = status;
  res.setHeader('Location', to);
  res.setHeader('Cache-Control', 'no-store');
  res.end();
}

function startFlow(req: Req, res: Res, p: SignInProvider, intent: 'signin' | 'link', next: string) {
  if (wrongAddress(req)) return fail(res, 409, 'wrong_address', `open Bothy at https://bothy.acme.com to sign in with ${names[p]}`, 'https://bothy.acme.com');
  if (!status(req, p).ready) return fail(res, 409, 'sign_in_unavailable', `${names[p]} sign-in isn't available right now`);
  state.flow = { p, intent, next };
  json(res, 200, { redirect_url: `/api/mock/oauth/${p}?state=mock` });
}

on('GET', '/api/auth/methods', (req, res) => json(res, 200, { providers: core.setup.needs_setup ? [] : offered(req) }), { open: true });
// Open routes match by exact path, so each provider gets its own.
for (const p of order) {
  on(
    'POST',
    `/api/auth/oauth/${p}/start`,
    async (req, res) => {
      const b = JSON.parse((await body(req)) || '{}');
      startFlow(req, res, p, 'signin', typeof b.next === 'string' && b.next.startsWith('/') && !b.next.startsWith('//') ? b.next : '/chat');
    },
    { open: true },
  );
  on('GET', `/api/mock/oauth/${p}`, (_req, res) => redirect(res, 302, `/api/auth/oauth/${p}/callback?code=mock&state=mock`), { open: true });
  on('GET', `/api/auth/oauth/${p}/callback`, (req, res) => callback(req, res, p), { open: true });
}

// The gateway's callback: always a 303 to a page of the app.
function callback(req: Req, res: Res, p: SignInProvider) {
  const flow = state.flow;
  state.flow = null;
  if (!flow || flow.p !== p) return redirect(res, 303, `/login?${new URLSearchParams({ signin_error: 'expired', provider: p })}`);
  const code = failWith(req) ? failWith.arg(req) || 'not_allowed' : '';
  const back = flow.intent === 'link' ? '/settings/account' : '/login';
  if (code) {
    const q = new URLSearchParams({ signin_error: code, provider: p });
    // The refused account rides in a cookie for a minute, never in the address.
    if (code === 'not_allowed') res.setHeader('Set-Cookie', `bothy_signin_who=${encodeURIComponent(p === 'github' ? 'kim-gh' : 'kim@gmail.com')}; Max-Age=60; Path=/; SameSite=Lax`);
    if (flow.intent === 'signin' && flow.next !== '/chat') q.set('next', flow.next);
    return redirect(res, 303, `${back}?${q}`);
  }
  if (flow.intent === 'link') {
    state.unlinked = state.unlinked.filter((x) => x !== p);
    const link: SignIn = { provider: p, user_id: core.me.id, created_at: nowISO(), last_used_at: nowISO() };
    if (p === 'google') link.email = 'sam@acme.com';
    else link.login = 'sam-acme';
    state.linked = [...state.linked.filter((l) => l.provider !== p), link];
    return redirect(res, 303, `/settings/account?connected=${p}`);
  }
  core.loggedIn = true;
  redirect(res, 303, flow.next);
}

on('GET', '/api/me/sign-ins', (req, res) => json(res, 200, { providers: offered(req), linked: linked(req), has_password: !onlyProvider(req) }));
on('POST', '/api/me/sign-ins/{p}', (req, res, m) => {
  const p = provider(res, m[1]);
  if (!p) return;
  if (linked(req).some((l) => l.provider === p)) return fail(res, 409, 'conflict', `you already connected ${names[p]}; disconnect it first`);
  startFlow(req, res, p, 'link', '');
});
on('DELETE', '/api/me/sign-ins/{p}', (req, res, m) => {
  const p = provider(res, m[1]);
  if (!p) return;
  const mine = linked(req);
  if (!mine.some((l) => l.provider === p)) return fail(res, 404, 'not_found', 'not found');
  if (onlyProvider(req) && mine.length === 1) return fail(res, 409, 'conflict', 'this is the only way you sign in; ask an admin for a password first');
  state.linked = state.linked.filter((l) => l.provider !== p);
  state.unlinked.push(p);
  json(res, 204);
});

// The server's checks (internal/config/signin.go), enough to show field errors.
const PUBLIC_MAIL = [
  'gmail.com', 'googlemail.com', 'outlook.com', 'hotmail.com', 'live.com', 'msn.com', 'yahoo.com', 'icloud.com', 'me.com', 'mac.com', 'aol.com',
  'proton.me', 'protonmail.com', 'gmx.com', 'mail.com', 'yandex.com', 'qq.com', 'users.noreply.github.com', 'duck.com', 'privaterelay.appleid.com',
  'pm.me', 'fastmail.com', 'hey.com', 'yahoo.co.uk', 'yahoo.co.jp', 'hotmail.co.uk', 'hotmail.fr', 'outlook.fr', 'live.co.uk', 'gmx.de', 'gmx.net',
  'web.de', 'mail.ru', '163.com', '126.com', 'naver.com', 'zoho.com', 'tutanota.com', 'yandex.ru', 'rambler.ru', 'libero.it', 'orange.fr', 'free.fr',
  't-online.de', 'btinternet.com', 'comcast.net', 'verizon.net', 'att.net',
];

function fieldError(res: Res, path: string, message: string) {
  json(res, 400, { error: { code: 'bad_request', message: `bothy.ui.yaml: ${path}: ${message}` }, fields: [{ path, source: { layer: 'ui' }, message: `bothy.ui.yaml: ${path}: ${message}` }] });
}

on('GET', '/api/sign-in', (req, res) => json(res, 200, admin(req)));
on('PATCH', '/api/sign-in', async (req, res) => {
  seed(req);
  const b = JSON.parse(await body(req));
  const set: Record<string, unknown> = {};
  if (Array.isArray(b.allow)) {
    const list = [...new Set((b.allow as string[]).map((e) => e.trim().toLowerCase().replace(/^@(?=[^@]+$)/, '')).filter(Boolean))];
    const bad = list.find((e) => PUBLIC_MAIL.includes(e));
    if (bad) return fieldError(res, 'auth.sign_in.allow', `anyone can get an address at ${bad}; list each person's address instead`);
    if (!list.length && state.providers.google.enabled) return fieldError(res, 'auth.sign_in.google.enabled', 'Google sign-in needs at least one address or domain in auth.sign_in.allow');
    const gh = state.providers.github;
    if (gh.enabled && !hasRules('github', list, gh.orgs))
      return fieldError(res, 'auth.sign_in.github.enabled', 'GitHub sign-in needs at least one email address in auth.sign_in.allow (domains apply to Google only), or an organization in auth.sign_in.github.orgs');
    state.allow = list;
    set['auth.sign_in.allow'] = list;
  }
  if (typeof b.allow_new === 'boolean') {
    state.allowNew = b.allow_new;
    set['auth.sign_in.allow_new'] = b.allow_new;
  }
  if (!Object.keys(set).length) return fail(res, 400, 'bad_request', 'nothing to change');
  writeConfig(set);
  json(res, 200, admin(req));
});
on('PATCH', '/api/sign-in/{p}', async (req, res, m) => {
  const p = provider(res, m[1]);
  if (!p) return;
  seed(req);
  const b = JSON.parse(await body(req));
  const c = state.providers[p];
  const base = `auth.sign_in.${p}`;
  if (b.orgs !== undefined && p !== 'github') return fail(res, 400, 'bad_request', 'organizations apply to GitHub sign-in only');
  // The server looks each organization up on GitHub to pin it by id; the mock knows all but no-such-org.
  const unknown = Array.isArray(b.orgs) ? (b.orgs as string[]).map((o) => o.trim()).find((o) => o.toLowerCase() === 'no-such-org') : undefined;
  if (unknown) {
    const message = `GitHub has no organization called ${unknown}.`;
    return json(res, 400, { error: { code: 'bad_request', message }, fields: [{ path: `${base}.orgs`, source: { layer: 'ui' }, message }] });
  }
  const next = {
    ...c,
    clientId: typeof b.client_id === 'string' ? b.client_id.trim() : c.clientId,
    secret: c.secret || !!b.client_secret,
    orgs: Array.isArray(b.orgs) ? (b.orgs as string[]).map((o) => o.trim()).filter(Boolean) : c.orgs,
    enabled: typeof b.enabled === 'boolean' ? b.enabled : c.enabled,
  };
  if (next.enabled) {
    const missing = [!next.clientId && `${names[p]} sign-in needs ${base}.client_id`, !next.secret && `${names[p]} sign-in needs ${base}.client_secret (a secret:<name> reference)`,
      !hasRules(p, state.allow, next.orgs) &&
        (p === 'github'
          ? 'GitHub sign-in needs at least one email address in auth.sign_in.allow (domains apply to Google only), or an organization in auth.sign_in.github.orgs'
          : 'Google sign-in needs at least one address or domain in auth.sign_in.allow'),
      address(publicURL(req)) !== 'ok' && `${names[p]} sign-in needs server.public_url`].filter(Boolean) as string[];
    if (missing.length) return fieldError(res, `${base}.enabled`, missing.join('; '));
  }
  state.providers[p] = next;
  const set: Record<string, unknown> = {};
  if (typeof b.enabled === 'boolean') set[`${base}.enabled`] = b.enabled;
  if (typeof b.client_id === 'string') set[`${base}.client_id`] = next.clientId;
  if (Array.isArray(b.orgs)) set[`${base}.orgs`] = next.orgs;
  if (b.client_secret) {
    const name = `${p}_sign_in_client_secret`;
    security.secrets = [...security.secrets.filter((s) => s.name !== name), { scope: 'global', name, source: 'vault', hint: String(b.client_secret).slice(-4), updated_at: nowISO(), last_used_at: ZERO, system: true }];
    set[`${base}.client_secret`] = `secret:${name}`;
  }
  if (!Object.keys(set).length) return fail(res, 400, 'bad_request', 'nothing to change');
  writeConfig(set);
  json(res, 200, admin(req));
});
on('POST', '/api/sign-in/{p}/check', (req, res, m) => {
  const p = provider(res, m[1]);
  if (!p) return;
  if (!checkFail(req)) return json(res, 200, { ok: true });
  const code = checkFail.arg(req) || 'key_rejected';
  const error = { key_rejected: 'oauth: google token: status 401: invalid_client', provider_error: 'oauth: github token: status 200: redirect_uri_mismatch', timeout: 'context deadline exceeded' }[code] ?? code;
  json(res, 200, { ok: false, code, error });
});
