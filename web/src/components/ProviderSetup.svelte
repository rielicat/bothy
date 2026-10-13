<script lang="ts">
  import { api, errorText } from '../lib/api/client';
  import type { ProbeResult, SecretMeta } from '../lib/api/types.gen';
  import { config } from '../lib/config.svelte';
  import { duration, tokens, usd } from '../lib/format';
  import { tiles, windowNote, type Tile } from '../lib/providers';
  import { toasts } from '../lib/stores/ui.svelte';
  import Icon from './Icon.svelte';

  let { onadded, preset }: { onadded?: (ref: string, probe: ProbeResult | null) => void; preset?: Partial<Tile> & { id: string } } = $props();

  let tile = $state<Tile | null>(null);
  let base = $state('');
  let key = $state('');
  let model = $state('');
  let allowPrivate = $state(false);
  let probe = $state<ProbeResult | null>(null);
  let busy = $state<'' | 'test' | 'save'>('');
  let error = $state('');
  let vault = $state<SecretMeta[]>([]);
  const id = $props.id();

  const stored = $derived(tile?.key ? vault.find((s) => s.name === tile!.key) : undefined);
  const needsKey = $derived(!!tile?.key && !tile.local && !tile.custom && !stored);

  // Refetched when a preset arrives: the wizard may just have imported a key.
  $effect(() => {
    if (preset) pick({ ...(tiles.find((t) => t.id === preset.id) ?? tiles[tiles.length - 1]), ...preset });
    api.get<SecretMeta[]>('/api/vault?scope=global').then((v) => (vault = v), () => {});
  });

  function pick(t: Tile) {
    tile = t;
    base = t.base ?? '';
    model = t.model;
    key = '';
    allowPrivate = !!t.local;
    probe = null;
    error = '';
  }

  async function test() {
    if (!tile) return;
    busy = 'test';
    error = '';
    try {
      probe = await api.post<ProbeResult>('/api/providers/test', {
        kind: tile.kind,
        base_url: base || undefined,
        api_key: key || undefined,
        secret_ref: !key && stored ? `secret:${stored.name}` : undefined,
        model,
        allow_private: allowPrivate,
      });
    } catch (e) {
      error = errorText(e);
    } finally {
      busy = '';
    }
  }

  async function use() {
    if (!tile) return;
    busy = 'save';
    try {
      if (key && tile.key) await api.put(`/api/vault/global/${encodeURIComponent(tile.key)}`, { value: key });
      const hasKey = !!key || !!stored;
      const errs = await config.set(
        {
          [`providers.${tile.id}`]: {
            id: tile.id,
            type: tile.kind,
            base_url: base,
            api_key: hasKey && tile.key ? `secret:${tile.key}` : '',
            allow_private: allowPrivate,
            discover: tile.kind === 'ollama',
          },
        },
        true,
      );
      if (errs) {
        error = errs.map((f) => f.message).join(' ');
        return;
      }
      toasts.ok(`${tile.name} is ready.`);
      onadded?.(`${tile.id}/${model}`, probe);
      key = '';
    } catch (e) {
      toasts.error(e);
    } finally {
      busy = '';
    }
  }
</script>

<div class="space-y-4">
  <div class="grid grid-cols-2 gap-2 sm:grid-cols-5" role="group" aria-label="Providers">
    {#each tiles as t (t.id)}
      <button
        type="button"
        class="card flex flex-col items-start gap-0.5 px-3 py-2.5 text-left text-sm hover:border-accent {tile?.id === t.id ? 'border-accent bg-accent-soft/60' : ''}"
        aria-pressed={tile?.id === t.id}
        onclick={() => pick(t)}
      >
        <span class="font-medium">{t.name}</span>
        <span class="text-xs text-muted">{t.local ? 'on your machine' : t.custom ? 'any OpenAI-compatible URL' : 'API key'}</span>
      </button>
    {/each}
  </div>

  {#if tile}
    <div class="card space-y-3 p-4">
      {#if tile.custom || tile.local}
        <div>
          <label class="label" for="{id}-base">Base URL</label>
          <input id="{id}-base" class="input mono" placeholder="https://llm.example.com/v1" bind:value={base} />
        </div>
      {/if}
      {#if tile.key}
        <div>
          <label class="label" for="{id}-key">API key {#if !needsKey}<span class="font-normal text-muted">(optional)</span>{/if}</label>
          <input id="{id}-key" class="input mono" type="password" autocomplete="off" spellcheck="false" placeholder={stored ? `Stored in the vault (…${stored.hint})` : 'Paste the key'} bind:value={key} />
          <p class="help">Stored encrypted in the vault and only ever sent to {tile.name}. It is never shown again.</p>
        </div>
      {/if}
      <div class="grid gap-3 sm:grid-cols-[1fr_auto]">
        <div>
          <label class="label" for="{id}-model">Model to test</label>
          <input id="{id}-model" class="input mono" list="{id}-models" placeholder="model id" bind:value={model} />
          <datalist id="{id}-models">{#each probe?.models ?? [] as m, i (i)}<option value={m}></option>{/each}</datalist>
        </div>
        {#if tile.local || tile.custom}
          <label class="flex items-center gap-2 self-end pb-2 text-sm"><input type="checkbox" class="switch" bind:checked={allowPrivate} />Private address</label>
        {/if}
      </div>
      <div class="flex flex-wrap gap-2">
        <button type="button" class="btn" disabled={!!busy || !model || (needsKey && !key)} onclick={test}>{busy === 'test' ? 'Testing…' : 'Test'}</button>
        <button type="button" class="btn btn-primary" disabled={!!busy || !model || !probe?.ok} onclick={use}>Use this provider</button>
      </div>
      {#if error}<p class="alert-bad" role="alert">{error}</p>{/if}
      {#if probe}
        <div class={probe.ok ? 'alert-ok flex-col' : 'alert-bad flex-col'} role="status">
          {#if probe.ok}
            <p class="flex flex-wrap gap-x-4 gap-y-1 font-medium">
              <span><Icon name="check" size={14} class="inline" /> key valid</span>
              <span><Icon name={probe.tool_calling ? 'check' : 'x'} size={14} class="inline" /> tool calling</span>
              {#if probe.context}<span>{tokens(probe.context)} context{windowNote(probe.context) ? ` · ${windowNote(probe.context)}` : ''}</span>{/if}
              <span>{duration(probe.latency)}</span>
              <span>test cost {usd(probe.cost_micros)}</span>
            </p>
          {/if}
          {#if probe.detail}<p>{probe.detail}</p>{/if}
        </div>
      {/if}
    </div>
  {/if}
</div>
