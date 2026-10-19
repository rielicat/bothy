import { describe, expect, it } from 'vitest';
import { failure, failureCode, failureCodes } from './providerErrors';

describe('failure categories (S13 and its fallback)', () => {
  it('uses the server code when it sends one', () => {
    for (const code of failureCodes) expect(failureCode({ code, detail: 'anything' })).toBe(code);
    expect(failureCode({ code: 'made_up', detail: '' })).toBe('unknown');
  });

  // Raw messages as the gateway and providers phrase them today.
  it.each([
    ['The provider rejected the API key: invalid x-api-key', 'key_rejected'],
    ['401: invalid x-api-key', 'key_rejected'],
    ['The provider rejected the test request: model: claude-x not found', 'no_model_access'],
    ['403: model not enabled for this account', 'no_model_access'],
    ['400: Your credit balance is too low to access the API.', 'out_of_credit'],
    ['The provider is rate limiting this key; try again shortly.', 'rate_limited'],
    ['The provider failed (overloaded): busy', 'provider_error'],
    ['Could not reach api.anthropic.com: context deadline exceeded', 'timeout'],
    ['Could not reach 10.0.0.12:443: dial tcp 10.0.0.12:443: connect: connection refused', 'unreachable'],
    ['Could not reach 127.0.0.1:9: destination address blocked: 127.0.0.1 resolves to 127.0.0.1 (loopback)', 'private_address'],
    ['The server lists no models; name one to test.', 'no_models'],
    ['x509: certificate signed by unknown authority', 'tls'],
    ['GET /v1/models: 404 page not found', 'not_a_model_server'],
    ['something odd', 'unknown'],
  ])('%s → %s', (detail, code) => expect(failureCode({ detail })).toBe(code));

  it('reads AWS messages only for Bedrock', () => {
    expect(failureCode({ detail: 'no EC2 IMDS role found', aws: true })).toBe('aws_no_credentials');
    expect(failureCode({ detail: 'AccessDeniedException: not authorized to perform: bedrock:ListFoundationModels', aws: true })).toBe('aws_denied');
    expect(failureCode({ detail: 'no Claude or Nova models are enabled in us-east-1', aws: true })).toBe('aws_no_models');
    expect(failureCode({ detail: 'AccessDeniedException' })).toBe('unknown');
  });

  it('falls back to the HTTP status', () => {
    expect(failureCode({ detail: '', status: 429 })).toBe('rate_limited');
    expect(failureCode({ detail: '', status: 504 })).toBe('timeout');
    expect(failureCode({ detail: '', status: 400 })).toBe('unknown');
  });
});

describe('failure copy (§5.6.4)', () => {
  const ctx = { name: 'Anthropic', model: 'Claude Sonnet 4.5', region: 'US East (N. Virginia)', source: 'instance role bothy-prod', hostPort: '127.0.0.1:9', console: 'https://console.anthropic.com' };

  // The action link follows the sentence ("… took too long to answer. Try again"), so the sentence never says it too.
  it('never repeats the action the link already gives', () => {
    for (const code of failureCodes) {
      const f = failure(code, { ...ctx, ollama: true });
      if (f.action?.kind === 'retry') expect(f.text).not.toMatch(new RegExp(f.action.label, 'i'));
    }
  });

  it('has a plain sentence for every code, with no raw error words', () => {
    for (const code of failureCodes) {
      const f = failure(code, ctx);
      expect(f.text).toMatch(/^[A-Z].*[.?]$/);
      expect(f.text).not.toMatch(/allow_private|egress|token|\b\d{3}\b|error:/i);
    }
  });

  it('names the provider, model and region where the plan does', () => {
    expect(failure('key_rejected', ctx)).toEqual({
      text: "That key wasn't accepted. Check you copied all of it, or create a new one.",
      action: { kind: 'link', label: 'Get a new key', href: 'https://console.anthropic.com' },
    });
    expect(failure('no_model_access', ctx).text).toBe("This key works, but it can't use Claude Sonnet 4.5. Choose another model.");
    expect(failure('out_of_credit', ctx).text).toBe('Your Anthropic account is out of credit. Add credit with Anthropic.');
    expect(failure('timeout', ctx).text).toBe('Anthropic took too long to answer.');
    expect(failure('unreachable', ctx).text).toBe("Bothy couldn't reach Anthropic from this server. Check the server's internet connection or firewall.");
    expect(failure('aws_no_models', ctx).text).toMatch(/in US East \(N\. Virginia\)\./);
    // The console link takes the region's code, never its readable name.
    expect(failure('aws_no_models', { ...ctx, regionCode: 'eu-west-1' }).action).toMatchObject({ href: 'https://console.aws.amazon.com/bedrock/home?region=eu-west-1#/modelaccess' });
    expect(failure('aws_denied', ctx).text).toBe("This server's AWS access isn't allowed to use Bedrock. Ask your AWS admin to allow it.");
  });

  it('blames the AWS secret, not the provider, for a key reference that fails', () => {
    for (const code of ['not_found', 'json_key_missing', 'not_json'] as const) {
      expect(failure(code, ctx).text).toMatch(/^(AWS|The secret in AWS) Secrets Manager /);
      expect(failure(code, ctx).text).not.toContain('Anthropic');
    }
  });

  it('offers to allow exactly the blocked host and port', () => {
    expect(failure('private_address', ctx).action).toEqual({ kind: 'allow', label: 'Allow 127.0.0.1:9' });
  });

  it('keeps technical detail for Details', () => {
    const local = failure('unreachable', { name: 'Ollama', local: true, ollama: true, hostPort: 'host.docker.internal:11434' });
    expect(local.text).toBe("Ollama isn't answering at that address. Make sure it's running.");
    expect(local.more?.[0]).toMatch(/OLLAMA_HOST=0\.0\.0\.0.*http:\/\/host\.docker\.internal:11434\.$/);
    expect(failure('aws_denied', ctx).more).toEqual(['Credentials from: instance role bothy-prod', expect.stringContaining('bedrock:InvokeModelWithResponseStream')]);
    const empty = failure('no_models', { name: 'Ollama', ollama: true });
    expect(empty.text).toBe(`Ollama is running but has no models yet. Install one on the server, for example ${empty.command}.`);
    expect(empty.command).toBe('ollama pull qwen3:8b');
    // The detected Ollama row already says "Running, but no models downloaded yet."
    expect(failure('no_models', { name: 'Ollama', ollama: true, emptySaid: true }).text).toBe('Install a model on the server, for example ollama pull qwen3:8b.');
    expect(failure('no_models', { name: 'LM Studio' }).text).toBe('LM Studio is running but has no models yet. Install one on the server.');
    expect(failure('no_models', { name: 'LM Studio' }).command).toBeUndefined();
  });

  it("doesn't tell Ollama to add /v1, which its own API has no use for", () => {
    expect(failure('not_a_model_server', { name: 'Ollama', ollama: true }).text).toBe("That address answered, but it isn't Ollama. Check the address and port; Ollama usually uses 11434.");
    expect(failure('not_a_model_server', { name: 'LM Studio' }).text).toMatch(/usually ends in \/v1\.$/);
  });

  it('talks about the secret, not Bedrock, when the key is an AWS Secrets Manager reference', () => {
    const ref = { name: 'OpenRouter', secretRef: true };
    expect(failure('aws_no_credentials', ref).text).toMatch(/^Bothy couldn't read this key from AWS Secrets Manager/);
    expect(failure('aws_denied', { name: 'Amazon Bedrock', source: 'access keys in the environment' })).toMatchObject({
      text: "This server's AWS access isn't allowed to use Bedrock. Ask your AWS admin to allow it.",
      more: expect.arrayContaining(['Credentials from: access keys in the environment']),
    });
    expect(failure('aws_no_credentials', ref).text).not.toContain('Bedrock');
    expect(failure('aws_denied', ref).text).not.toContain('Bedrock');
    expect(failure('aws_denied', ref).more).toEqual([expect.stringContaining('secretsmanager:GetSecretValue')]);
  });
});
