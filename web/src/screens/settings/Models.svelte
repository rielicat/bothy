<script lang="ts">
  import ChainEditor from '../../components/ChainEditor.svelte';
  import Icon from '../../components/Icon.svelte';
  import ProviderSetup from '../../components/ProviderSetup.svelte';
  import Section from '../../components/Section.svelte';
  import { api, errorText } from '../../lib/api/client';
  import type { Detection, ModelInfo, ProbeResult, ProviderHealth } from '../../lib/api/types.gen';
  import { config } from '../../lib/config.svelte';
  import { duration } from '../../lib/format';
  import { toasts } from '../../lib/stores/ui.svelte';

  let models = $state<ModelInfo[]>([]);
  let health = $state<ProviderHealth[]>([]);
  let detected = $state<Detection[] | null>(null);
  let adding = $state(false);
  let probes = $state<Record<string, ProbeResult | string>>({});
  let price = $state({ ref: '', inp: '', out: '' });

  const ids = $derived(config.keys('providers'));
  const prices = $derived(config.keys('pricing', 'tail'));

  async function refresh() {
    [models, health] = await Promise.all([api.get<ModelInfo[]>('/api/models').catch(() => []), api.get<ProviderHealth[]>('/api/providers').catch(() => [])]);
  }

  $effect(() => {
    void refresh();
  });

  async function test(id: string) {
    const chain = [...config.value<string[]>('models.main', []), ...config.value<string[]>('models.fast', [])];
    const model = chain.find((r) => r.startsWith(id + '/'))?.slice(id.length + 1) ?? config.keys(`providers.${id}.models`, 'tail')[0] ?? '';
    probes[id] = 'testing';
    try {
      probes[id] = await api.post<ProbeResult>('/api/providers/test', {
        kind: config.value(`providers.${id}.type`),
        base_url: config.value(`providers.${id}.base_url`) || undefined,
        secret_ref: config.value(`providers.${id}.api_key`) || undefined,
        model,
        allow_private: config.value(`providers.${id}.allow_private`, false),
      });
    } catch (e) {
      probes[id] = errorText(e);
    }
  }

  async function detect() {
    detected = null;
    try {
      detected = await api.post<Detection[]>('/api/providers/detect');
    } catch (e) {
      toasts.error(e);
      detected = [];
    }
  }

  async function addPrice(e: SubmitEvent) {
    e.preventDefault();
    if (!(await config.set({ [`pricing.${price.ref}`]: { in: Number(price.inp), out: Number(price.out) } }))) price = { ref: '', inp: '', out: '' };
  }
</script>

<Section title="Providers" desc="Where Bothy sends model requests, using your own keys.">
  {#snippet actions()}
    <button type="button" class="btn btn-sm" onclick={() => (adding = !adding)}><Icon name="plus" size={14} />Add provider</button>
  {/snippet}
  {#if adding}
    <div class="py-4">
      <ProviderSetup
        onadded={() => {
          adding = false;
          void refresh();
        }}
      />
    </div>
  {/if}
  {#each ids as id (id)}
    {@const h = health.find((x) => x.id === id)}
    {@const p = probes[id]}
    {@const enabled = config.value(`providers.${id}.enabled`, null) !== false}
    <div class="flex flex-wrap items-center gap-3 py-3.5">
      <input
        type="checkbox"
        class="switch"
        checked={enabled}
        aria-label="Enable {id}"
        disabled={config.entry(`providers.${id}.enabled`)?.locked}
        onchange={(e) => config.set({ [`providers.${id}.enabled`]: e.currentTarget.checked })}
      />
      <div class="min-w-0 flex-1">
        <p class="font-medium">{id} <span class="text-xs font-normal text-muted">{config.value(`providers.${id}.type`)}</span></p>
        <p class="mono truncate text-xs text-muted">{config.value(`providers.${id}.base_url`) || 'default endpoint'}</p>
        {#if h?.last_error}<p class="text-xs text-bad">{h.last_error}</p>{/if}
        {#if typeof p === 'string' && p !== 'testing'}<p class="text-xs text-bad">{p}</p>
        {:else if p && typeof p === 'object'}<p class="text-xs {p.ok ? 'text-ok' : 'text-bad'}">{p.ok ? `Works · ${duration(p.latency)} · tools ${p.tool_calling ? 'yes' : 'no'}` : 'Failed'}{p.detail ? ` · ${p.detail}` : ''}</p>{/if}
      </div>
      {#if h}<span class="chip {h.state === 'ok' ? 'chip-ok' : h.state === 'degraded' ? 'chip-warn' : 'chip-bad'}">{h.state}</span>{/if}
      <button type="button" class="btn btn-sm" disabled={p === 'testing'} onclick={() => test(id)}>{p === 'testing' ? 'Testing…' : 'Test'}</button>
      {#if !config.entry(`providers.${id}.id`)?.locked}
        <button type="button" class="btn btn-ghost btn-icon btn-sm" aria-label="Remove {id}" title="Remove the web UI entry" onclick={() => config.set({ [`providers.${id}`]: null })}><Icon name="trash" size={14} /></button>
      {/if}
    </div>
  {:else}
    <p class="py-6 text-center text-sm text-muted">No providers yet. Add one to start chatting.</p>
  {/each}
</Section>

<Section title="Model chains" desc="Bothy tries models in order and falls back before the first word streams. Drag or use the arrows to reorder.">
  <ChainEditor path="models.main" label="Main" help="Everyday replies and tool use." {models} />
  <ChainEditor path="models.fast" label="Fast" help="Titles, summaries, heartbeats and cheap schedules." {models} />
</Section>

<Section title="Local models" desc="Ollama, LM Studio and vLLM running next to Bothy.">
  {#snippet actions()}<button type="button" class="btn btn-sm" onclick={detect}><Icon name="search" size={14} />Detect</button>{/snippet}
  {#if detected === null}
    <p class="py-4 text-sm text-muted">Press Detect to look for local servers and API keys in the environment.</p>
  {:else if !detected.length}
    <p class="py-4 text-sm text-muted">Nothing found.</p>
  {:else}
    {#each detected as d, i (i)}
      <div class="flex flex-wrap items-center gap-3 py-3 text-sm">
        <Icon name={d.env_var ? 'key' : 'monitor'} size={16} class="text-muted" />
        <p class="min-w-0 flex-1">{d.env_var ? `${d.env_var} found in the environment` : `${d.kind} at ${d.base_url}`}{d.models?.length ? `: ${d.models.length} models` : ''}{d.note ? ` · ${d.note}` : ''}</p>
      </div>
    {/each}
  {/if}
</Section>

<Section title="Price overrides" desc="USD per million tokens; overrides the built-in catalog, for example 0 for local models.">
  {#each prices as ref (ref)}
    <div class="flex items-center gap-3 py-3 text-sm">
      <span class="mono min-w-0 flex-1 truncate">{ref}</span>
      <span class="tabular-nums text-muted">in ${config.value(`pricing.${ref}.in`, 0)} · out ${config.value(`pricing.${ref}.out`, 0)}</span>
      <button type="button" class="btn btn-ghost btn-icon btn-sm" aria-label="Remove price for {ref}" onclick={() => config.set({ [`pricing.${ref}`]: null })}><Icon name="trash" size={14} /></button>
    </div>
  {/each}
  <form class="grid gap-2 py-4 sm:grid-cols-[1fr_6rem_6rem_auto]" onsubmit={addPrice}>
    <input class="input mono" placeholder="provider/model" aria-label="Model" required bind:value={price.ref} />
    <input class="input" type="number" min="0" step="any" placeholder="in $/M" aria-label="Input price per million tokens" required bind:value={price.inp} />
    <input class="input" type="number" min="0" step="any" placeholder="out $/M" aria-label="Output price per million tokens" required bind:value={price.out} />
    <button class="btn">Add</button>
  </form>
</Section>
