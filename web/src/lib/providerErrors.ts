// Plain copy for a failed model check (docs/design/ui.md §5.6.4). The server
// sends a category code (S13); until it does, the raw message is matched
// here, which is the only place allowed to read provider error text.

export const failureCodes = [
  'key_rejected',
  'no_model_access',
  'out_of_credit',
  'rate_limited',
  'provider_error',
  'timeout',
  'unreachable',
  'private_address',
  'no_models',
  'tls',
  'not_a_model_server',
  'aws_no_credentials',
  'aws_denied',
  'aws_no_models',
  'not_found',
  'json_key_missing',
  'not_json',
  'unknown',
] as const;
export type FailureCode = (typeof failureCodes)[number];

const isCode = (c: string | undefined): c is FailureCode => !!c && (failureCodes as readonly string[]).includes(c);

// Order matters: a blocked private address also says "could not reach", and
// AWS denials mention models.
const patterns: [FailureCode, RegExp, ('aws' | 'other')?][] = [
  ['private_address', /destination address blocked|private address|allow_private|private network|loopback/],
  ['tls', /x509|certificate|tls handshake/],
  ['aws_no_credentials', /no ec2 imds|no valid credential|could not find credentials|nocredentialprovider|failed to retrieve credentials|credentials not found|no credentials/, 'aws'],
  ['aws_denied', /accessdenied|access denied|not authorized to perform/, 'aws'],
  ['aws_no_models', /no claude or nova|model access|no models are enabled/, 'aws'],
  ['timeout', /deadline exceeded|timed? ?out/],
  ['no_models', /lists no models|no models/],
  ['out_of_credit', /credit|insufficient_quota|quota|billing|payment required|\b402\b/],
  ['rate_limited', /rate limit|too many requests|\b429\b/],
  ['key_rejected', /rejected the api key|api key|x-api-key|\b401\b|unauthori[sz]ed|authentication/],
  ['not_a_model_server', /no responses api|page not found|unexpected content|invalid character/],
  ['no_model_access', /model.*(not found|does not exist|not enabled|not available|no access|not allowed)|\b403\b/],
  ['not_a_model_server', /\b404\b|not found/],
  ['unreachable', /could not reach|connection refused|no such host|dial tcp|unreachable|connect:|eof/],
  ['provider_error', /\b5\d\d\b|overloaded|server error|the provider failed/],
];

/** The failure's category: the server's code when it sent one, else a match on the raw message. */
export function failureCode(o: { code?: string; detail?: string; status?: number; aws?: boolean }): FailureCode {
  if (isCode(o.code)) return o.code;
  const text = (o.detail ?? '').toLowerCase();
  for (const [code, re, only] of patterns) {
    if (only === 'aws' && !o.aws) continue;
    if (re.test(text)) return code;
  }
  if (o.status === 429) return 'rate_limited';
  if (o.status === 504) return 'timeout';
  return 'unknown';
}

export type FailureAction =
  | { kind: 'retry'; label: string }
  | { kind: 'link'; label: string; href: string }
  | { kind: 'allow'; label: string }
  | { kind: 'models' };

export interface Failure {
  text: string;
  /** A command inside `text`, shown in mono. */
  command?: string;
  action?: FailureAction;
  /** Lines added under the raw message in Details. */
  more?: string[];
}

export interface FailureContext {
  /** The provider's or server's name: "Anthropic", "Ollama". */
  name: string;
  /** A server on this computer or the network, not a cloud provider. */
  local?: boolean;
  model?: string;
  /** The region's readable name for the sentence, and its code for the console link. */
  region?: string;
  regionCode?: string;
  /** Where the AWS credentials come from, never their value: "instance role bothy-prod". */
  source?: string;
  /** For "Allow {host:port}". */
  hostPort?: string;
  /** Where to get a new key. */
  console?: string;
  ollama?: boolean;
  /** The key is an aws-sm reference, so AWS failures are about reading the secret, not Bedrock. */
  secretRef?: boolean;
}

const OLLAMA_PULL = 'ollama pull qwen3:8b';

export const AWS_HELP = 'https://github.com/rielicat/bothy/blob/main/docs/deploy/README.md';

export function failure(code: FailureCode, c: FailureContext): Failure {
  const retry: FailureAction = { kind: 'retry', label: 'Try again' };
  const aws = c.source ? [`Credentials from: ${c.source}`] : [];
  switch (code) {
    case 'key_rejected':
      return { text: "That key wasn't accepted. Check you copied all of it, or create a new one.", action: c.console ? { kind: 'link', label: 'Get a new key', href: c.console } : undefined };
    case 'no_model_access':
      return { text: `This key works, but it can't use ${c.model || 'that model'}. Choose another model.`, action: { kind: 'models' } };
    case 'out_of_credit':
      return { text: `Your ${c.name} account is out of credit. Add credit with ${c.name}, then try again.`, action: retry };
    case 'rate_limited':
      return { text: `${c.name} is limiting requests right now. Wait a minute and try again.`, action: retry };
    case 'provider_error':
      return { text: `${c.name} isn't responding properly right now. Try again in a few minutes.`, action: retry };
    case 'timeout':
      return { text: `${c.name} took too long to answer. Try again.`, action: retry };
    case 'unreachable':
      if (!c.local) return { text: `Bothy couldn't reach ${c.name} from this server. Check the server's internet connection or firewall.`, action: retry };
      return {
        text: `${c.name} isn't answering at that address. Make sure it's running.`,
        action: retry,
        more: [
          `If Bothy runs in Docker, ${c.name} must accept connections from other computers (for Ollama: OLLAMA_HOST=0.0.0.0) and the address is usually http://host.docker.internal:${c.hostPort?.split(':').pop() || (c.ollama ? '11434' : '1234')}.`,
        ],
      };
    case 'private_address':
      return { text: 'Bothy blocks addresses on your private network unless you allow them.', action: c.hostPort ? { kind: 'allow', label: `Allow ${c.hostPort}` } : undefined };
    case 'no_models':
      return {
        text: `${c.name} is running but has no models yet. Install one on the server${c.ollama ? `, for example ${OLLAMA_PULL},` : ','} then check again.`,
        command: c.ollama ? OLLAMA_PULL : undefined,
        action: { kind: 'retry', label: 'Check again' },
      };
    case 'tls':
      return { text: "The connection to that address isn't secure because of a certificate problem. Check the address. For a server on your own network you can use http://." };
    case 'not_a_model_server':
      return { text: "That address answered, but it doesn't look like a model server. Check the address; it usually ends in /v1." };
    case 'aws_no_credentials':
      if (c.secretRef)
        return {
          text: "Bothy couldn't read this key from AWS Secrets Manager because this server doesn't have AWS access right now. On AWS, give it a role that can read the secret. On a laptop, sign in with the AWS CLI again.",
          action: { kind: 'link', label: 'How to give Bothy AWS access', href: AWS_HELP },
          more: aws,
        };
      return {
        text: "This server doesn't have AWS access right now. On AWS, give it a role with Bedrock access. On a laptop, sign in with the AWS CLI and restart Bothy.",
        action: { kind: 'link', label: 'How to give Bothy AWS access', href: AWS_HELP },
        more: aws,
      };
    case 'aws_denied':
      if (c.secretRef)
        return {
          text: `This server's AWS access isn't allowed to read that secret. Ask your AWS admin to let ${c.source || 'its role'} read it.`,
          more: [...aws, 'Reading a secret needs: secretsmanager:GetSecretValue, and kms:Decrypt if the secret uses its own encryption key'],
        };
      return {
        text: `This server's AWS access isn't allowed to use Bedrock. Ask your AWS admin to add Bedrock permissions to ${c.source || 'its role'}.`,
        more: [...aws, 'Bedrock needs: bedrock:InvokeModelWithResponseStream, bedrock:ListFoundationModels, bedrock:ListInferenceProfiles'],
      };
    case 'aws_no_models':
      return {
        text: `No Claude or Nova models are turned on for this AWS account in ${c.region || 'this region'}. Turn on model access in the Bedrock console, or choose another region.`,
        action: { kind: 'link', label: 'Open the Bedrock console', href: `https://console.aws.amazon.com/bedrock/home?region=${encodeURIComponent(c.regionCode ?? 'us-east-1')}#/modelaccess` },
        more: aws,
      };
    // Only an aws-sm key reference fails with these (SPEC §14.8); the server's
    // sentence naming the reference goes to Details.
    case 'not_found':
      return { text: "AWS Secrets Manager has no secret by that name that this server can read. Check the secret's name and region.", action: retry };
    case 'json_key_missing':
      return { text: "The secret in AWS Secrets Manager doesn't have the value this setting asks for. Check the name after # in the reference.", action: retry };
    case 'not_json':
      return { text: "The secret in AWS Secrets Manager holds a single value, so Bothy can't pick a named part of it. Remove the part after # from the reference.", action: retry };
    default:
      return { text: 'Something went wrong while checking. Try again, or choose another option.', action: retry, more: aws };
  }
}
