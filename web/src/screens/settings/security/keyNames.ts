// Saved keys by friendly name and by what uses them (§11.6): the stored name
// "anthropic_api_key" reads "Anthropic API key · used by Anthropic".
import type { Entry } from '../../../lib/api/types.gen';

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
  return services[id] ?? id.charAt(0).toUpperCase() + id.slice(1);
}

const AWS_SM = 'aws-sm:';

/** "anthropic_api_key" → "Anthropic API key"; unknown names read as words. */
export function keyName(name: string): string {
  if (name.startsWith(AWS_SM)) return awsSecretName(name);
  const lower = name.toLowerCase();
  const [prefix] = lower.split(/[_.-]/);
  for (const [re, kind] of kinds) {
    if (re.test(lower) && services[prefix] && lower.replace(re, '') === prefix) return `${services[prefix]} ${kind}`;
  }
  const words = lower.replace(/[_.-]+/g, ' ').replace(/\bapi\b/g, 'API').trim();
  return words.charAt(0).toUpperCase() + words.slice(1);
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

// A reference may sit at the end of a header value: "Bearer aws-sm:docs#token".
const pointsAt = (value: unknown, name: string) =>
  name.startsWith(AWS_SM) ? typeof value === 'string' && (value === name || value.endsWith(` ${name}`)) : value === `secret:${name}`;

/** What uses a saved key, from the settings that point at `secret:<name>` (or at the aws-sm reference). */
export function usedBy(name: string, entries: Entry[]): string[] {
  const out = new Set<string>();
  for (const e of entries) {
    if (!pointsAt(e.value, name)) continue;
    const [area, id] = e.path.split('.');
    if (area === 'providers' || area === 'channels') out.add(serviceName(id));
    else if (area === 'search') out.add(serviceName(id.split('_')[0]));
    else if (area === 'mcp') out.add(e.path.split('.')[2] ?? 'a connected tool');
    else out.add(serviceName(area));
  }
  return [...out];
}

/** "Anthropic", "Slack and Discord", "Anthropic, OpenAI and Groq". */
export function joinNames(names: string[]): string {
  if (names.length <= 1) return names[0] ?? '';
  return `${names.slice(0, -1).join(', ')} and ${names[names.length - 1]}`;
}

/** The name stored in the vault for what someone typed: "Brave Search key" → "brave_search_key". */
export function storedName(typed: string): string {
  return typed
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9._-]+/g, '_')
    .replace(/^_+|_+$/g, '');
}
