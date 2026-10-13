<script lang="ts">
  import { onMount } from 'svelte';
  import ChainEditor from '../../components/ChainEditor.svelte';
  import Icon from '../../components/Icon.svelte';
  import ProviderSetup from '../../components/ProviderSetup.svelte';
  import { api } from '../../lib/api/client';
  import type { Detection, ModelInfo } from '../../lib/api/types.gen';
  import { config } from '../../lib/config.svelte';
  import { tiles, type Tile } from '../../lib/providers';
  import { attempt } from '../../lib/stores/ui.svelte';

  let { onnext }: { onnext: () => void } = $props();

  let detections = $state<Detection[] | null>(null);
  let models = $state<ModelInfo[]>([]);
  let preset = $state<(Partial<Tile> & { id: string }) | undefined>();
  let imported = $state<string[]>([]);

  const main = $derived(config.value<string[]>('models.main', []) ?? []);

  async function loadModels() {
    models = await api.get<ModelInfo[]>('/api/models').catch(() => []);
  }

  // Once per visit: detection probes several local ports, and an effect here
  // would re-run on every config change the step itself makes.
  onMount(() => {
    void config.load();
    void loadModels();
    api.post<Detection[]>('/api/providers/detect').then((d) => (detections = d), () => (detections = []));
  });

  async function importKey(d: Detection) {
    const t = tiles.find((x) => x.env === d.env_var);
    if (!t?.key) return;
    if (await attempt(() => api.post('/api/vault/import', { env_var: d.env_var, name: t.key }), `${d.env_var} is now in the vault.`)) {
      imported = [...imported, d.env_var!];
      preset = { id: t.id };
    }
  }

  function useLocal(d: Detection) {
    const t = tiles.find((x) => x.kind === d.kind && x.local) ?? tiles.find((x) => x.id === 'custom')!;
    preset = { id: t.id, base: d.base_url, model: d.models?.[0] ?? t.model, local: true };
  }

  async function added(ref: string) {
    await loadModels();
    const [provider] = ref.split('/');
    const t = tiles.find((x) => x.id === provider);
    const set: Record<string, unknown> = {};
    if (!main.length) set['models.main'] = [ref];
    if (!config.value<string[]>('models.fast', [])?.length) set['models.fast'] = [t?.fast ? `${provider}/${t.fast}` : ref];
    if (Object.keys(set).length) await config.set(set, true);
  }
</script>

<h1 class="text-2xl font-semibold tracking-tight">Connect a model</h1>
<p class="mt-2 text-muted">Bring your own key, or use a model running on this machine. Keys are encrypted and only sent to their provider.</p>

{#if detections === null}
  <p class="mt-6 flex items-center gap-2 text-sm text-muted"><span class="size-2 animate-pulse rounded-full bg-accent"></span>Looking for local models and keys…</p>
{:else if detections.length}
  <ul class="mt-6 space-y-2">
    {#each detections as d, i (i)}
      <li class="flex flex-wrap items-center gap-3 rounded-xl border border-accent/30 bg-accent-soft/40 px-4 py-3 text-sm">
        <Icon name={d.env_var ? 'key' : 'monitor'} size={18} class="text-accent" />
        <span class="min-w-0 flex-1">
          {#if d.env_var}<strong>{d.env_var}</strong> found. Import it into the vault?{:else}<strong>{d.kind}</strong> at {d.base_url.replace(/^https?:\/\//, '')}{d.models?.length ? `: ${d.models.length} models` : ''}{/if}
        </span>
        {#if d.env_var}
          <button type="button" class="btn btn-sm" disabled={imported.includes(d.env_var)} onclick={() => importKey(d)}>{imported.includes(d.env_var) ? 'Imported' : 'Import'}</button>
        {:else}
          <button type="button" class="btn btn-sm" onclick={() => useLocal(d)}>Use</button>
        {/if}
      </li>
    {/each}
  </ul>
{/if}

<div class="mt-6"><ProviderSetup {preset} onadded={added} /></div>

{#if main.length}
  <div class="mt-8 rounded-xl border border-line px-4">
    <ChainEditor path="models.main" label="Main model" help="Everyday replies and tool use. Add fallbacks for when it is down." {models} />
    <div class="border-t border-line"><ChainEditor path="models.fast" label="Fast model" help="Titles, summaries and heartbeats; cheaper is better." {models} /></div>
  </div>
{/if}

<div class="mt-8 flex justify-end">
  <button type="button" class="btn btn-primary" disabled={!main.length} onclick={onnext}>Continue</button>
</div>
