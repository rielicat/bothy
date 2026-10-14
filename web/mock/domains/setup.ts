// Setup and sign-in (C2): the setup code, the owner account, signing in, and
// the provider detection and checks the model step runs.
import type { Detection, ProbeResult } from '../../src/lib/api/types.gen.ts';
import { body, core, fail, json, on, SETUP_TOKEN, scenario, sleep } from './core.ts';
import { me } from './shell.ts';

// Local until S3 and S13 land in types.gen.ts (docs/design/ui.md §17); C10 removes them.
export type DetectionV2 = Detection & { region?: string; source?: string };
export type ProbeResultV2 = ProbeResult & { code?: string };

export const detections: Detection[] = [
  { kind: 'ollama', base_url: 'http://host.docker.internal:11434', models: ['qwen3:14b', 'qwen3:4b', 'llama3.2:3b'], note: '3 models' },
  { kind: 'anthropic', base_url: '', env_var: 'ANTHROPIC_API_KEY' },
];

const awsDetection: DetectionV2 = {
  kind: 'bedrock',
  base_url: '',
  region: 'us-east-1',
  source: 'instance role bothy-prod',
  models: ['us.anthropic.claude-sonnet-4-5-20250929-v1:0', 'us.anthropic.claude-haiku-4-5-20251001-v1:0', 'us.amazon.nova-pro-v1:0'],
};

const detectNone = scenario('detect-none', 'Setup finds no local models, keys or cloud access.', 'setup');
const detectOllama = scenario('detect-ollama', 'Setup finds only Ollama on this computer.', 'setup');
const detectAws = scenario('detect-aws', "Setup finds this server's AWS access (Bedrock, S3 shape).", 'setup');
const providerFail = scenario('provider-fail', 'Every provider check fails; provider-fail:<code> picks the S13 code (default key_rejected).', 'setup');

// Raw messages as providers phrase them; the UI maps `code`, never the text.
const failures: Record<string, string> = {
  key_rejected: '401: invalid x-api-key',
  no_model_access: '403: model not enabled for this account',
  out_of_credit: '400: Your credit balance is too low to access the API.',
  rate_limited: '429: rate limit exceeded',
  provider_error: '500: internal server error',
  timeout: 'context deadline exceeded (Client.Timeout exceeded while awaiting headers)',
  unreachable: 'dial tcp 10.0.0.12:443: connect: connection refused',
  private_address: 'egress: 127.0.0.1:9 is a private address (providers[].allow_private)',
  no_models: 'the server answered but lists no models',
  tls: 'x509: certificate signed by unknown authority',
  not_a_model_server: 'GET /v1/models: 404 page not found',
  aws_no_credentials: 'no EC2 IMDS role found, operation error ec2imds: GetMetadata',
  aws_denied: 'AccessDeniedException: not authorized to perform bedrock:ListFoundationModels',
  aws_no_models: 'no Claude or Nova models are enabled in us-east-1',
};

on('GET', '/api/setup/state', (_q, res) => json(res, 200, core.setup), { open: true });
on(
  'POST',
  '/api/setup/claim',
  async (req, res) => {
    const { token } = JSON.parse(await body(req));
    if (token !== SETUP_TOKEN) return fail(res, 401, 'unauthorized', 'That setup token is not valid or has expired.', 'Run `docker exec bothy /bothy setup-token` to print a new one.');
    core.setup.claimed = true;
    core.setup.csrf = 'mock-csrf';
    json(res, 200, { csrf: 'mock-csrf' });
  },
  { open: true },
);
on(
  'POST',
  '/api/setup/owner',
  async (req, res) => {
    const b = JSON.parse(await body(req));
    if (b.password === 'password1234') return fail(res, 400, 'bad_request', 'This password is on the list of common passwords. Pick something less guessable.');
    core.me = { ...core.me, username: b.username, display_name: b.display_name || b.username, timezone: b.timezone };
    core.setup.owner_set = true;
    delete core.setup.csrf;
    core.loggedIn = true;
    json(res, 200, me(req));
  },
  { open: true },
);
on('POST', '/api/setup/complete', (_q, res) => {
  // Like the gateway, setup routes are gone once setup is done.
  if (!core.setup.needs_setup) return fail(res, 404, 'not_found', 'not found');
  core.setup.needs_setup = false;
  json(res, 204);
});
on(
  'POST',
  '/api/auth/login',
  async (req, res) => {
    const b = JSON.parse(await body(req));
    if (!b.username || b.password === 'wrong') return fail(res, 401, 'unauthorized', 'Wrong username or password.');
    core.loggedIn = true;
    json(res, 200, me(req));
  },
  { open: true },
);
on('POST', '/api/auth/password', (_q, res) => json(res, 204));

on('POST', '/api/providers/test', async (req, res) => {
  const b = JSON.parse(await body(req));
  await sleep(900);
  const code = providerFail(req) ? providerFail.arg(req) || 'key_rejected' : b.api_key === 'bad' ? 'key_rejected' : '';
  if (code) {
    const r: ProbeResultV2 = { ok: false, latency: 300e6, tool_calling: false, usage_reported: false, context: 0, models: [], cost_micros: 0, detail: failures[code] ?? code, code };
    return json(res, 200, r);
  }
  if (b.kind === 'ollama') return json(res, 200, { ok: true, latency: 480e6, tool_calling: true, usage_reported: true, context: 4096, models: ['qwen3:14b', 'qwen3:4b'], cost_micros: 0, detail: 'Ollama serves qwen3:14b with 4,096 context; set OLLAMA_CONTEXT_LENGTH=16384 or pick a model with ≥ 16K' });
  json(res, 200, { ok: true, latency: 820e6, tool_calling: true, usage_reported: true, context: 200000, models: ['claude-sonnet-4-6', 'claude-haiku-4-5'], cost_micros: 31, detail: '' });
});
on('POST', '/api/providers/detect', async (req, res) => {
  await sleep(600);
  if (detectNone(req)) return json(res, 200, []);
  if (detectAws(req)) return json(res, 200, [awsDetection]);
  if (detectOllama(req)) return json(res, 200, detections.filter((d) => d.kind === 'ollama'));
  json(res, 200, detections);
});
