// Saved keys by friendly name and by what uses them (§11.6): the stored name
// "anthropic_api_key" reads "Anthropic API key · used by Anthropic".
import type { Entry } from '../../../lib/api/types.gen';
// A connected tool is named as Connections names it ("DeepWiki", "Acme helpdesk").
import { capital, serviceName as toolName } from '../../../lib/format';

const services: Record<string, string> = {
  anthropic: 'Anthropic',
  openai: 'OpenAI',
  gemini: 'Google Gemini',
  google: 'Google',
  openrouter: 'OpenRouter',
  groq: 'Groq',
  xai: 'xAI',
  deepseek: 'DeepSeek',
  mistral: 'Mistral',
  ollama: 'Ollama',
  bedrock: 'Amazon Bedrock',
  aws: 'AWS',
  telegram: 'Telegram',
  slack: 'Slack',
  discord: 'Discord',
  brave: 'Brave Search',
  tavily: 'Tavily',
  searxng: 'SearXNG',
  github: 'GitHub',
};

const kinds: [RegExp, string][] = [
  [/_api_key$/, 'API key'],
  [/_bot_token$/, 'bot token'],
  [/_app_token$/, 'app token'],
  [/_token$/, 'token'],
  [/_secret$/, 'secret'],
  [/_password$/, 'password'],
  [/_key$/, 'key'],
];

export function serviceName(id: string): string {
  return services[id] ?? capital(id);
}

const AWS_SM = 'aws-sm:';

// Keys Bothy stores for itself carry an id nobody should have to read: a
// GitHub connection's token or app key ("github_ghc_01…"), an app Bothy
// created ("github_gha_01…_key", "…_client_secret") and sign-in secrets.
const own: [RegExp, string, string][] = [
  [/^github_ghc_[0-9a-z]+$/, 'GitHub connection key', 'GitHub'],
  [/^github_gha_[0-9a-z]+_key$/, "GitHub app's private key", 'GitHub'],
  [/^github_gha_[0-9a-z]+_client_secret$/, "GitHub app's client secret", 'GitHub'],
  [/^google_sign_in_client_secret$/, 'Google sign-in client secret', 'Sign-in'],
  [/^github_sign_in_client_secret$/, 'GitHub sign-in client secret', 'Sign-in'],
];

/** "anthropic_api_key" → "Anthropic API key"; unknown names read as words. */
export function keyName(name: string): string {
  if (name.startsWith(AWS_SM)) return awsSecretName(name);
  const lower = name.toLowerCase();
  const mine = own.find(([re]) => re.test(lower));
  if (mine) return mine[1];
  // The secret header the Add an MCP server dialog saves.
  const mcp = /^mcp_([a-z0-9-]+)_header$/.exec(lower);
  if (mcp) return `Key for ${toolName(mcp[1])}`;
  const [prefix] = lower.split(/[_.-]/);
  for (const [re, kind] of kinds) {
    if (re.test(lower) && services[prefix] && lower.replace(re, '') === prefix) return `${services[prefix]} ${kind}`;
  }
  const words = lower.replace(/[_.-]+/g, ' ').replace(/\bapi\b/g, 'API').trim();
  if (services[prefix]) return services[prefix] + words.slice(prefix.length);
  return capital(words);
}

// The global key list also carries the server's aws-sm references (SPEC
// §14.8), named by the reference itself. An ARN shows only the secret's name,
// never the account: "aws-sm:prod/openai#api_key" → "prod/openai (api_key) in
// AWS Secrets Manager".
function awsSecretName(ref: string): string {
  const [id, key] = ref.slice(AWS_SM.length).split('?')[0].split('#');
  const at = id.indexOf(':secret:');
  const secret = id.startsWith('arn:') && at >= 0 ? id.slice(at + ':secret:'.length) : id;
  return `${secret}${key ? ` (${key})` : ''} in AWS Secrets Manager`;
}

// A reference may sit inside a value, as in "Bearer secret:docs_token" or an
// address's "?api_key=secret:docs_key", as long as it stands as a word.
const pointsAt = (value: unknown, name: string) =>
  typeof value === 'string' && ` ${value} `.replace(/[=&]/g, ' ').includes(` ${name.startsWith(AWS_SM) ? name : `secret:${name}`} `);

/** What uses a saved key, from the settings that point at `secret:<name>` (or at the aws-sm reference). */
export function usedBy(name: string, entries: Entry[]): string[] {
  const out = new Set<string>();
  const mine = own.find(([re]) => re.test(name.toLowerCase()));
  // GitHub's own keys are referenced from its connection, not from a setting.
  if (mine && mine[2] === 'GitHub') return [mine[2]];
  for (const e of entries) {
    if (!pointsAt(e.value, name)) continue;
    const [area, id] = e.path.split('.');
    if (area === 'providers' || area === 'channels') out.add(serviceName(id));
    else if (area === 'search') out.add(serviceName(id.split('_')[0]));
    else if (area === 'mcp') out.add(toolName(e.path.split('.')[2] ?? 'a connected tool'));
    else if (area === 'auth') out.add('Sign-in');
    else out.add(serviceName(area));
  }
  return [...out];
}

/** The name stored in the vault for what someone typed: "Brave Search key" → "brave_search_key". */
export function storedName(typed: string): string {
  return typed
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9._-]+/g, '_')
    .replace(/^_+|_+$/g, '');
}
