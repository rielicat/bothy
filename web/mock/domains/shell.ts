// Shell (C1): who is signed in, the event stream, banners and pausing.
import type { BannerData, Me } from '../../src/lib/api/types.gen.ts';
import { active, body, boot, clients, core, emit, isPaused, json, on, pausedNow, principals, scenario, scenarios, viewer, type Req } from './core.ts';

const sealed = scenario('sealed', "Bothy's saved keys are locked (vault sealed banner).", 'shell');
const providerAuth = scenario('provider-auth', 'Anthropic stopped accepting the key (provider banner).', 'shell');
const safeMode = scenario('safe-mode', 'Bothy started with safe defaults after a settings error (banner).', 'shell');
const restart = scenario('restart', 'A changed setting needs a restart (banner).', 'shell');
const noBanners = scenario('no-banners', 'Drop the default banners.', 'shell');

/** The banners this request should see: stored ones plus the scenario ones. */
export function banners(req?: Req): BannerData[] {
  const out = noBanners(req) ? [] : [...core.banners];
  if (pausedNow(req) && !out.some((b) => b.code === 'paused')) out.push({ level: 'error', code: 'paused', text: 'Bothy is paused. Every run was stopped; only admins can resume.' });
  if (sealed(req)) out.push({ level: 'error', code: 'vault_sealed', text: 'The vault is sealed.', action: '/settings/security/keys' });
  if (providerAuth(req)) out.push({ level: 'error', code: 'provider_auth', text: 'anthropic: 401 invalid x-api-key', action: '/settings/models' });
  if (safeMode(req)) out.push({ level: 'error', code: 'safe_mode', text: 'Started in safe mode: bothy.ui.yaml could not be loaded.', action: '/settings/advanced' });
  if (restart(req)) out.push({ level: 'info', code: 'restart_required', text: 'Restart to apply: sandbox.backend' });
  return out;
}

export function me(req?: Req): Me {
  return { user: viewer(req), csrf: 'mock-csrf', mode: core.mode, principals, paused: isPaused(req), sandbox_available: true, banners: banners(req), access: { peer: 'local', tls: false } };
}

on('GET', '/api/auth/me', (req, res) => json(res, 200, me(req)));
on('POST', '/api/auth/logout', (_q, res) => {
  core.loggedIn = false;
  json(res, 204);
});

on('GET', '/api/events', (req, res) => {
  res.statusCode = 200;
  res.setHeader('Content-Type', 'text/event-stream');
  res.setHeader('Cache-Control', 'no-store');
  res.write(`event: hello\ndata: ${JSON.stringify({ boot, seq: core.seq, paused: isPaused(req), sandbox: true, banners: banners(req) })}\n\n`);
  clients.add(res);
  const ping = setInterval(() => res.write(': ping\n\n'), 15000);
  req.on('close', () => {
    clearInterval(ping);
    clients.delete(res);
  });
});

on('POST', '/api/admin/pause', async (req, res) => {
  core.paused = JSON.parse(await body(req)).paused;
  const b: BannerData = core.paused ? { level: 'error', code: 'paused', text: 'Bothy is paused. Every run was stopped; only admins can resume.' } : { level: 'error', code: 'paused', text: '', clear: true };
  core.banners = core.paused ? [...core.banners, b] : core.banners.filter((x) => x.code !== 'paused');
  emit('banner', b);
  json(res, 204);
});

// Not a gateway route: lists the scenario switches for people and the shot specs.
on('GET', '/api/mock/scenarios', (req, res) => json(res, 200, { active: Object.fromEntries(active(req)), scenarios: scenarios() }), { open: true });
