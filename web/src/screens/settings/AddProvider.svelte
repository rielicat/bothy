<script lang="ts">
  import { Button, KeyField, LinkButton, OptionList, Select, StatusLine, TextField, type CheckResult, type Option } from '../../components/ui';
  import { api } from '../../lib/api/client';
  import { failureCode, failureText, regions, type ProbeResultV2 } from './modelHelpers';
  import { settingsConfig } from './store.svelte';

  // Add a provider from Settings → Models, used only until ProviderChooser
  // (C2) is in the tree: the provider list of setup step 2 with its automatic
  // checks, saving on the first successful one (§11.4). C10 deletes it.
  let { onconnected }: { onconnected?: (name: string) => void } = $props();

  interface Choice {
    id: string;
    name: string;
    kind: string;
    line: string;
    /** Model the check asks; the server lists the rest. */
    model: string;
    base?: string;
    console?: string;
    local?: boolean;
    custom?: boolean;
    more?: boolean;
  }

  const all: Choice[] = [
    { id: 'anthropic', name: 'Anthropic', kind: 'anthropic', line: 'Claude models', model: 'claude-sonnet-4-6', console: 'https://console.anthropic.com' },
    { id: 'openai', name: 'OpenAI', kind: 'openai', line: 'GPT models', model: 'gpt-5.1', base: 'https://api.openai.com/v1', console: 'https://platform.openai.com/api-keys' },
    { id: 'gemini', name: 'Google Gemini', kind: 'gemini', line: 'Gemini models', model: 'gemini-2.5-pro', console: 'https://aistudio.google.com' },
    { id: 'openrouter', name: 'OpenRouter', kind: 'openai_compat', line: 'Many models with one key', model: 'anthropic/claude-sonnet-4.5', base: 'https://openrouter.ai/api/v1', console: 'https://openrouter.ai/keys' },
    { id: 'bedrock', name: 'Amazon Bedrock', kind: 'bedrock', line: "Uses this server's AWS access", model: '' },
    { id: 'groq', name: 'Groq', kind: 'openai_compat', line: 'Fast open models', model: 'llama-3.3-70b-versatile', base: 'https://api.groq.com/openai/v1', console: 'https://console.groq.com/keys', more: true },
    { id: 'xai', name: 'xAI', kind: 'openai_compat', line: 'Grok models', model: 'grok-4', base: 'https://api.x.ai/v1', console: 'https://console.x.ai', more: true },
    { id: 'deepseek', name: 'DeepSeek', kind: 'openai_compat', line: 'DeepSeek models', model: 'deepseek-chat', base: 'https://api.deepseek.com/v1', console: 'https://platform.deepseek.com', more: true },
    { id: 'ollama', name: 'Ollama', kind: 'ollama', line: 'On this computer or your network', model: '', base: 'http://host.docker.internal:11434', local: true, more: true },
    { id: 'lmstudio', name: 'LM Studio', kind: 'openai_compat', line: 'On this computer or your network', model: '', base: 'http://host.docker.internal:1234/v1', local: true, more: true },
    { id: 'custom', name: 'Other server', kind: 'openai_compat', line: 'Any OpenAI-compatible server', model: '', custom: true, more: true },
  ];

  let more = $state(false);
  let chosen = $state('');
  let key = $state('');
  let address = $state('');
  let region = $state('us-east-1');
  let allowPrivate = $state(false);
  let addressCheck = $state<{ tone: 'progress' | 'danger'; text: string; details?: string; host?: string } | null>(null);
  let timer: ReturnType<typeof setTimeout> | undefined;
  let ctrl: AbortController | null = null;

  const existing = $derived(new Set(settingsConfig.keys('providers')));
  // Providers already in the list are managed there, so they aren't offered again.
  const options: Option[] = $derived(all.filter((c) => (more || !c.more) && !existing.has(c.id)).map((c) => ({ value: c.id, title: c.name, description: c.line })));
  const choice = $derived(all.find((c) => c.id === chosen));

  function pick() {
    clearTimeout(timer);
    ctrl?.abort();
    key = '';
    allowPrivate = false;
    addressCheck = null;
    address = choice?.base ?? '';
    if (choice?.local) void checkAddress();
  }

  // The usual local address is allowed as it stands; any other private address needs "Allow" (D15).
  function allowed(c: Choice): boolean {
    return allowPrivate || (!!c.local && address.trim() === c.base);
  }

  function probeBody(extra: Record<string, unknown> = {}) {
    const c = choice!;
    return {
      kind: c.kind,
      base_url: (c.local || c.custom ? address.trim() : c.base) || undefined,
      model: c.model,
      allow_private: allowed(c),
      region: c.kind === 'bedrock' ? region : undefined,
      ...extra,
    };
  }

  /** Stores the key, writes the provider and, when no model is chosen yet, makes its first model the main one. */
  async function save(c: Choice, probe: ProbeResultV2, apiKey: string) {
    const secret = `${c.id}_api_key`;
    if (apiKey) await api.put(`/api/vault/global/${encodeURIComponent(secret)}`, { value: apiKey });
    const set: Record<string, unknown> = {
      [`providers.${c.id}`]: {
        id: c.id,
        type: c.kind,
        base_url: c.local || c.custom ? address.trim() : (c.base ?? ''),
        api_key: apiKey ? `secret:${secret}` : '',
        allow_private: allowed(c),
        discover: c.kind === 'ollama',
        ...(c.kind === 'bedrock' ? { region } : {}),
      },
    };
    const first = probe.models?.[0] || c.model;
    if (first && !(settingsConfig.value<string[]>('models.main', []) ?? []).length) set['models.main'] = [`${c.id}/${first}`];
    await settingsConfig.save(set);
    onconnected?.(c.name);
  }

  function failure(c: Choice, r: { code?: string; detail?: string }): string {
    return failureText(failureCode(r), c.name, c.local ? 'ollama' : c.kind);
  }

  async function checkKey(value: string, signal: AbortSignal): Promise<CheckResult> {
    const c = choice!;
    const r = await api.post<ProbeResultV2>('/api/providers/test', probeBody({ api_key: value }));
    if (signal.aborted) return { ok: false, message: '' };
    if (!r.ok) return { ok: false, message: failure(c, r), details: r.detail || undefined };
    await save(c, r, value);
    return { ok: true, message: `Connected to ${c.name}.` };
  }

  async function checkAddress() {
    clearTimeout(timer);
    ctrl?.abort();
    const c = choice;
    if (!c || (c.kind !== 'bedrock' && !address.trim())) return (addressCheck = null);
    const me = new AbortController();
    ctrl = me;
    addressCheck = { tone: 'progress', text: c.kind === 'bedrock' ? 'Checking AWS access…' : 'Checking…' };
    try {
      const r = await api.post<ProbeResultV2>('/api/providers/test', probeBody(c.custom && key ? { api_key: key } : {}));
      if (me.signal.aborted) return;
      if (r.ok) return void (await save(c, r, c.custom ? key : ''));
      const code = failureCode(r);
      let host: string | undefined;
      try {
        host = code === 'private_address' ? new URL(address.trim()).host : undefined;
      } catch {
        host = undefined;
      }
      addressCheck = { tone: 'danger', text: failure(c, r), details: r.detail || undefined, host };
    } catch (e) {
      if (!me.signal.aborted) addressCheck = { tone: 'danger', text: failureText('unknown', c.name), details: e instanceof Error ? e.message : String(e) };
    }
  }

  function typedAddress() {
    clearTimeout(timer);
    addressCheck = null;
    timer = setTimeout(() => void checkAddress(), 800);
  }

  $effect(() => () => {
    clearTimeout(timer);
    ctrl?.abort();
  });
</script>

<OptionList name="add-provider" label="Providers" framed={false} {options} bind:value={chosen} onchange={pick}>
  {#snippet expanded()}
    {#if choice}
      {@const c = choice}
      <div class="grid gap-4">
        {#if c.kind === 'bedrock'}
          <p class="text-body text-text">Bothy uses the AWS access of the server it runs on, so there's no key to paste. No keys are stored in Bothy.</p>
          <Select label="Region" bind:value={region} options={regions.map((r) => ({ value: r.code, label: r.name }))} onchange={() => (addressCheck = null)} />
          <div class="flex flex-wrap items-center gap-x-4 gap-y-2">
            <Button onclick={checkAddress}>Check access</Button>
            <a class="link text-body" href="https://github.com/rielicat/bothy/tree/main/docs/deploy" target="_blank" rel="noopener noreferrer">How to give Bothy AWS access ↗</a>
          </div>
        {:else if c.local || c.custom}
          <TextField label="Server address" bind:value={address} placeholder={c.custom ? 'https://models.example.com/v1' : undefined} inputmode="url" oninput={typedAddress} />
          {#if c.custom}
            <TextField label="API key (if needed)" type="password" bind:value={key} autocomplete="off" oninput={typedAddress} />
          {/if}
        {:else}
          <KeyField label="API key" bind:value={key} check={checkKey} checkingLabel="Checking the key…">
            {#snippet help()}
              Stored encrypted. Only sent to {c.name}. <a class="link" href={c.console} target="_blank" rel="noopener noreferrer">Get a key at {new URL(c.console ?? 'https://x').host} ↗</a>
            {/snippet}
          </KeyField>
        {/if}
        {#if addressCheck}
          <StatusLine
            tone={addressCheck.tone}
            text={addressCheck.text}
            details={addressCheck.details}
            alert
            action={addressCheck.host
              ? {
                  label: `Allow ${addressCheck.host}`,
                  onclick: () => {
                    allowPrivate = true;
                    void checkAddress();
                  },
                }
              : addressCheck.tone === 'danger'
                ? { label: 'Try again', onclick: checkAddress }
                : undefined}
          />
        {/if}
      </div>
    {/if}
  {/snippet}
</OptionList>
{#if !more}
  <LinkButton class="mt-3 text-body" onclick={() => (more = true)}>More providers</LinkButton>
{/if}
