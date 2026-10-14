<script lang="ts" module>
  export interface ManagedProvider {
    id: string;
    kind: string;
    name: string;
    enabled: boolean;
    baseUrl: string;
    keyRef: string;
    allowPrivate: boolean;
    api: string;
    region: string;
    health?: ProviderHealth;
    /** The model the check asks, from the slots or the provider's own list. */
    model: string;
    /** Why it can't be removed, when it can't. */
    keep?: string;
  }
</script>

<script lang="ts">
  import { untrack } from 'svelte';
  import { Button, Dialog, Disclosure, KeyField, LinkButton, Select, StatusLine, toast, type CheckResult } from '../../components/ui';
  import { api, ApiError } from '../../lib/api/client';
  import type { ProbeResult, ProviderHealth } from '../../lib/api/types.gen';
  import { duration } from '../../lib/format';
  import { failureCode } from '../../lib/providerErrors';
  import ConfirmDialog from './ConfirmDialog.svelte';
  import { failureText, regionName, regions } from './modelHelpers';
  import { Saver, settingsConfig } from './store.svelte';

  // Manage one provider (§11.4): its state, a new key, the region for
  // Bedrock, what the last check found, and removing it.
  let { open = $bindable(false), provider, onchanged }: { open?: boolean; provider: ManagedProvider | null; onchanged?: () => void } = $props();

  type Check = { state: 'checking' } | { state: 'done'; probe: ProbeResult } | { state: 'error'; message: string };
  let check = $state<Check | null>(null);
  let key = $state('');
  let confirmOpen = $state(false);
  let removing = $state(false);
  let removeError = $state('');
  const regionSaver = new Saver();
  let ctrl: AbortController | null = null;

  const p = $derived(provider);
  const hasKey = $derived(!!p && p.kind !== 'bedrock' && p.kind !== 'ollama' && (!!p.keyRef || !p.allowPrivate));
  const vaultName = $derived(p?.keyRef.startsWith('secret:') ? p.keyRef.slice(7) : `${p?.id}_api_key`);

  function body(extra: Record<string, unknown> = {}) {
    if (!p) return {};
    return {
      kind: p.kind,
      base_url: p.baseUrl || undefined,
      secret_ref: p.keyRef || undefined,
      model: p.model,
      allow_private: p.allowPrivate,
      api: p.api || undefined,
      region: p.kind === 'bedrock' ? p.region : undefined,
      ...extra,
    };
  }

  async function runCheck() {
    ctrl?.abort();
    const c = new AbortController();
    ctrl = c;
    check = { state: 'checking' };
    try {
      const probe = await api.post<ProbeResult>('/api/providers/test', body());
      if (!c.signal.aborted) check = { state: 'done', probe };
    } catch (e) {
      if (!c.signal.aborted) check = { state: 'error', message: e instanceof ApiError ? e.message : String(e) };
    }
  }

  // Checks once per opening: the provider object is rebuilt whenever the page
  // refreshes its data, which must not start another check or clear the key field.
  const pid = $derived(p?.id);
  $effect(() => {
    const shown = open && !!pid;
    untrack(() => {
      if (!shown || !p) {
        ctrl?.abort();
        return;
      }
      key = '';
      removeError = '';
      if (p.enabled) void runCheck();
      else check = null;
    });
  });

  const probeCode = (r: ProbeResult) => failureCode({ code: r.code, detail: r.detail, aws: p?.kind === 'bedrock' });

  const status = $derived.by((): { tone: 'progress' | 'success' | 'danger'; text: string; details?: string } | null => {
    if (!p || !check) return null;
    if (check.state === 'checking') return { tone: 'progress', text: p.kind === 'bedrock' ? 'Checking AWS access…' : 'Checking…' };
    if (check.state === 'error') return { tone: 'danger', text: failureText('unknown', p.name, p.kind), details: check.message };
    const r = check.probe;
    if (r.ok) {
      const n = r.models?.length ?? 0;
      return { tone: 'success', text: n ? `Connected. Bothy can use ${n === 1 ? '1 model' : `${n} models`} here.` : 'Connected.' };
    }
    return { tone: 'danger', text: failureText(probeCode(r), p.name, p.kind), details: r.detail || undefined };
  });

  async function turnOn() {
    if (!p) return;
    try {
      await settingsConfig.save({ [`providers.${p.id}.enabled`]: true });
      onchanged?.();
      void runCheck();
    } catch {
      check = { state: 'error', message: "Bothy couldn't turn it on." };
    }
  }

  async function checkKey(value: string, signal: AbortSignal): Promise<CheckResult> {
    if (!p) return { ok: false, message: failureText('unknown', '') };
    const r = await api.post<ProbeResult>('/api/providers/test', body({ api_key: value, secret_ref: undefined }));
    if (signal.aborted) return { ok: false, message: '' };
    if (!r.ok) return { ok: false, message: failureText(probeCode(r), p.name, p.kind, p.model), details: r.detail || undefined };
    await api.put(`/api/vault/global/${encodeURIComponent(vaultName)}`, { value });
    if (p.keyRef !== `secret:${vaultName}`) await settingsConfig.save({ [`providers.${p.id}.api_key`]: `secret:${vaultName}` });
    check = { state: 'done', probe: r };
    onchanged?.();
    return { ok: true, message: 'Connected. Bothy uses the new key from now on.' };
  }

  async function changeRegion(region: string) {
    if (!p) return;
    if (await regionSaver.run(() => settingsConfig.save({ [`providers.${p.id}.region`]: region }))) {
      onchanged?.();
      void runCheck();
    }
  }

  async function remove() {
    if (!p) return;
    removing = true;
    removeError = '';
    const strip = (path: string) => (settingsConfig.value<string[]>(path, []) ?? []).filter((r) => !r.startsWith(`${p.id}/`));
    try {
      await settingsConfig.save({ [`providers.${p.id}`]: null, 'models.main': strip('models.main'), 'models.fast': strip('models.fast') });
      removing = false;
      confirmOpen = false;
      open = false;
      toast(`${p.name} removed.`);
      onchanged?.();
    } catch {
      removing = false;
      removeError = `Bothy couldn't remove ${p.name}. Try again.`;
    }
  }

  const probe = $derived(check?.state === 'done' ? check.probe : null);
  // The raw message of a failed check sits in Details, once, with the rest of what the check found.
  const lastError = $derived(status?.details ?? p?.health?.last_error);
</script>

<Dialog bind:open title={p?.name ?? ''} size="md">
  {#if p}
    <div class="grid gap-5">
      {#if !p.enabled}
        <p class="text-body text-text">Off. Bothy doesn't use this provider. <LinkButton class="ml-1" onclick={turnOn}>Turn on</LinkButton></p>
      {:else if status}
        <StatusLine tone={status.tone} text={status.text} action={status.tone === 'danger' ? { label: 'Try again', onclick: runCheck } : undefined} />
      {/if}

      {#if hasKey}
        <div>
          <KeyField label="API key" bind:value={key} saved={!!p.keyRef} check={checkKey} checkingLabel="Checking the key…" help="Stored encrypted. Only sent to {p.name}." />
          {#if p.health?.source}<p class="mt-2 text-small text-muted">Credentials from: {p.health.source}</p>{/if}
        </div>
      {/if}

      {#if p.kind === 'bedrock'}
        <div>
          <Select
            label="Region"
            value={p.region}
            options={regions.map((r) => ({ value: r.code, label: r.name }))}
            error={regionSaver.error || undefined}
            onchange={(e: Event) => changeRegion((e.currentTarget as HTMLSelectElement).value)}
          />
          {#if p.health?.source}<p class="mt-2 text-small text-muted">Credentials from: {p.health.source}</p>{/if}
        </div>
      {/if}

      <Disclosure>
        <dl class="grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-1 text-small">
          {#if p.baseUrl}<dt class="text-muted">Address</dt><dd class="font-mono break-all text-text">{p.baseUrl}</dd>{/if}
          {#if p.kind === 'bedrock'}<dt class="text-muted">Region</dt><dd class="text-text">{regionName(p.region)} ({p.region})</dd>{/if}
          {#if p.model}<dt class="text-muted">Checked with</dt><dd class="font-mono break-all text-text">{p.model}</dd>{/if}
          {#if probe?.context}<dt class="text-muted">Context window</dt><dd class="text-text tabular-nums">{probe.context.toLocaleString()} tokens</dd>{/if}
          {#if probe?.latency}<dt class="text-muted">Answered in</dt><dd class="text-text">{duration(probe.latency)}</dd>{/if}
          {#if probe}<dt class="text-muted">Tool use</dt><dd class="text-text">{probe.tool_calling ? 'Yes' : 'No'}</dd>{/if}
          {#if lastError}<dt class="text-muted">Last error</dt><dd class="font-mono break-all text-text">{lastError}</dd>{/if}
        </dl>
      </Disclosure>

      <div class="flex flex-col items-start gap-1 border-t border-border pt-4">
        <!-- Enabled, only the words show, so they take the content edge; disabled, the grey box does. -->
        <Button variant="danger-text" class={p.keep ? '' : '-ml-4'} reason={p.keep} onclick={() => (confirmOpen = true)}>Remove provider…</Button>
      </div>
    </div>
  {/if}
  {#snippet footer({ close })}
    <Button onclick={close}>Done</Button>
  {/snippet}
</Dialog>

<ConfirmDialog
  bind:open={confirmOpen}
  title="Remove {p?.name}?"
  confirm="Remove provider"
  busyLabel="Removing…"
  busy={removing}
  error={removeError}
  onconfirm={remove}>Bothy stops using {p?.name} and its models. Its saved key stays in Saved keys.</ConfirmDialog
>
