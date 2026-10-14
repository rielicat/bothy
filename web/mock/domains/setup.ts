// Setup and sign-in (C2): the setup code, the owner account, signing in, and
// the provider detection and checks the model step runs.
import type { Detection, FailureCode, ProbeResult } from '../../src/lib/api/types.gen.ts';
import { body, core, fail, json, on, SETUP_TOKEN, scenario, sleep, type Req } from './core.ts';
import { me } from './shell.ts';


export const detections: Detection[] = [
  { kind: 'ollama', base_url: 'http://host.docker.internal:11434', models: ['qwen3:14b', 'qwen3:4b', 'llama3.2:3b'], note: '3 models' },
  { kind: 'anthropic', base_url: '', env_var: 'ANTHROPIC_API_KEY' },
];

const bedrockModels = ['us.anthropic.claude-sonnet-4-5-20250929-v1:0', 'us.anthropic.claude-haiku-4-5-20251001-v1:0', 'us.amazon.nova-pro-v1:0'];
const awsDetection: Detection = { kind: 'bedrock', base_url: '', region: 'us-east-1', source: 'instance role bothy-prod', models: bedrockModels };

const detectNone = scenario('detect-none', 'Setup finds no local models, keys or cloud access.', 'setup');
const detectOllama = scenario('detect-ollama', 'Setup finds only Ollama on this computer.', 'setup');
const detectAws = scenario('detect-aws', "Setup finds this server's AWS access (Bedrock, S3 shape).", 'setup');
const detectSlow = scenario('detect-slow', 'Detection takes 20 s, to show "Looking for models on this server…".', 'setup');
const ollamaEmpty = scenario('ollama-empty', 'The detected Ollama has no models downloaded.', 'setup');
const checkSlow = scenario('check-slow', 'Every provider check takes 20 s, to show the checking line.', 'setup');
const providerFail = scenario('provider-fail', 'Every provider check fails; provider-fail:<code> picks the S13 code (default key_rejected).', 'setup');
const modelContext = scenario('model-context', 'Checks report this context window (model-context:12000) for the small-model notes.', 'setup');
const noTools = scenario('no-tools', 'Checks report a model that cannot call tools.', 'setup');
const noModelList = scenario('no-model-list', 'An address check answers with an empty model list, so setup asks for the model by name.', 'setup');
const install = scenario('install', 'How Bothy was installed (install:docker, install:binary); compose by default.', 'setup');
const setupOwner = scenario('setup-owner', 'The owner exists but setup is unfinished (use with logged-out for the sign-in notice).', 'setup');
const remote = scenario('remote', 'This browser is on another computer: setup routes answer 403.', 'setup');
const claimLimit = scenario('claim-limit', 'Too many setup code tries (429).', 'setup');
const grantExpired = scenario('grant-expired', 'The setup code grant expired before the account was created (401).', 'setup');
const loginLocked = scenario('login-locked', 'Sign in is locked out for a few minutes (429).', 'setup');
const loginDisabled = scenario('login-disabled', 'The account signing in is turned off.', 'setup');
const mustChange = scenario('must-change', 'Sign in returns must_change_password (S9): the account has a one-time password.', 'setup');

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
  aws_denied: 'AccessDeniedException: User: arn:aws:sts::123456789012:assumed-role/bothy-prod is not authorized to perform: bedrock:ListFoundationModels',
  aws_no_models: 'no Claude or Nova models are enabled in us-east-1',
};

const REMOTE_FIX = 'ssh -L 8080:127.0.0.1:8080 <host>, then open the printed setup URL on http://127.0.0.1:8080';
const remoteFail = (res: Parameters<typeof json>[0]) => fail(res, 403, 'forbidden', 'setup is only available from this machine', REMOTE_FIX);

on(
  'GET',
  '/api/setup/state',
  (req, res) => {
    if (remote(req)) return remoteFail(res);
    const state = setupOwner(req) ? { ...core.setup, needs_setup: true, owner_set: true } : core.setup;
    json(res, 200, { ...state, install: install.arg(req) || 'compose' });
  },
  { open: true },
);
on(
  'POST',
  '/api/setup/claim',
  async (req, res) => {
    if (remote(req)) return remoteFail(res);
    if (claimLimit(req)) return fail(res, 429, 'rate_limited', 'too many setup claims');
    const { token } = JSON.parse(await body(req));
    if (token !== SETUP_TOKEN) return fail(res, 401, 'unauthorized', 'invalid or expired setup token');
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
    if (grantExpired(req)) return fail(res, 401, 'unauthorized', 'setup session expired: claim the setup token again');
    if (b.password === 'password1234') return fail(res, 400, 'bad_request', 'this password is too common or predictable; a few unrelated words work well');
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
    if (loginLocked(req)) return fail(res, 429, 'rate_limited', 'too many failed sign-in attempts: try again in 4m30s');
    if (loginDisabled(req)) return fail(res, 403, 'forbidden', 'account disabled');
    if (!b.username || b.password === 'wrong') return fail(res, 401, 'unauthorized', 'invalid username or password');
    core.loggedIn = true;
    const m = me(req);
    json(res, 200, { ...m, user: { ...m.user, must_change_password: mustChange(req) || undefined } });
  },
  { open: true },
);
on('POST', '/api/auth/password', async (req, res) => {
  const b = JSON.parse(await body(req));
  if ([...(b.new ?? '')].length < 12) return fail(res, 400, 'bad_request', 'password must be at least 12 characters');
  if (b.new === 'password1234') return fail(res, 400, 'bad_request', 'this password is too common or predictable; a few unrelated words work well');
  json(res, 204);
});

// Hosts a real gateway refuses unless the provider allows private addresses.
const privateHost = (url: string) => {
  try {
    return /^(localhost|127\.|10\.|192\.168\.|172\.(1[6-9]|2\d|3[01])\.|host\.docker\.internal$|.*\.local$)/.test(new URL(url).hostname);
  } catch {
    return false;
  }
};

function catalog(kind: string, base: string): string[] {
  if (kind === 'bedrock') return bedrockModels;
  if (kind === 'anthropic') return ['claude-sonnet-4-6', 'claude-haiku-4-5-20251001', 'claude-opus-4-1-20250805'];
  if (kind === 'openai') return ['gpt-5.1', 'gpt-5-mini', 'gpt-4.1'];
  if (kind === 'gemini') return ['gemini-2.5-pro', 'gemini-2.5-flash'];
  if (kind === 'ollama') return ['qwen3:14b', 'qwen3:4b', 'llama3.2:3b'];
  if (base.includes('openrouter')) return ['anthropic/claude-sonnet-4.5', 'anthropic/claude-haiku-4.5', 'openai/gpt-5.1'];
  if (base.includes('groq')) return ['llama-3.3-70b-versatile', 'qwen/qwen3-32b'];
  if (base.includes('x.ai')) return ['grok-4', 'grok-4-fast'];
  if (base.includes('deepseek')) return ['deepseek-chat', 'deepseek-reasoner'];
  if (base.includes(':1234')) return ['qwen2.5-7b-instruct', 'llama-3.1-8b-instruct'];
  return ['llama-3.3-70b-instruct'];
}

function probe(req: Req, b: { kind: string; base_url?: string; api_key?: string; model?: string; allow_private?: boolean; region?: string }): ProbeResult {
  const base = b.base_url ?? '';
  const failed = (code: FailureCode): ProbeResult => ({
    ok: false,
    latency: 300e6,
    tool_calling: false,
    usage_reported: false,
    context: 0,
    models: code === 'no_model_access' ? catalog(b.kind, base) : [],
    cost_micros: 0,
    detail: failures[code] ?? code,
    error: failures[code] ?? code,
    code,
    credentials: b.kind === 'bedrock' ? 'instance role bothy-prod' : undefined,
  });
  const code = (providerFail(req) ? providerFail.arg(req) || 'key_rejected' : b.api_key === 'bad' ? 'key_rejected' : '') as FailureCode | '';
  if (code) return failed(code);
  if (base && privateHost(base) && !b.allow_private) return { ...failed('private_address'), error: `Could not reach ${new URL(base).host}: destination address blocked` };
  const models = noModelList(req) && b.kind === 'openai_compat' ? [] : ollamaEmpty(req) && b.kind === 'ollama' ? [] : catalog(b.kind, base);
  if (!models.length && !b.model) return { ...failed('no_models'), error: 'The server lists no models; name one to test.' };
  const context = Number(modelContext.arg(req)) || (b.kind === 'ollama' ? 32768 : 200000);
  return { ok: true, latency: 820e6, tool_calling: !noTools(req) && context >= 8192, usage_reported: true, context, models, cost_micros: b.kind === 'ollama' ? 0 : 31, detail: '' };
}

on('POST', '/api/providers/test', async (req, res) => {
  const b = JSON.parse(await body(req));
  await sleep(checkSlow(req) ? 20000 : 900);
  if (!['anthropic', 'openai', 'gemini', 'ollama', 'openai_compat', 'bedrock'].includes(b.kind)) return fail(res, 400, 'bad_request', `unknown provider kind "${b.kind}"`);
  json(res, 200, probe(req, b));
});
on('POST', '/api/providers/detect', async (req, res) => {
  await sleep(detectSlow(req) ? 20000 : 600);
  if (detectNone(req)) return json(res, 200, []);
  if (detectAws(req)) return json(res, 200, [awsDetection]);
  const ollama = detections.filter((d) => d.kind === 'ollama').map((d) => (ollamaEmpty(req) ? { ...d, models: [] } : d));
  if (detectOllama(req) || ollamaEmpty(req)) return json(res, 200, ollama);
  json(res, 200, [...ollama, ...detections.filter((d) => d.kind !== 'ollama')]);
});
