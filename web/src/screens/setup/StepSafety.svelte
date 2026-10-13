<script lang="ts">
  import { onMount } from 'svelte';
  import PresetPicker from '../../components/PresetPicker.svelte';
  import SandboxStatus from '../../components/SandboxStatus.svelte';
  import { api } from '../../lib/api/client';
  import type { ModelInfoX } from '../../lib/api/extra';
  import { config } from '../../lib/config.svelte';
  import { toasts } from '../../lib/stores/ui.svelte';

  let { onnext }: { onnext: () => void } = $props();

  let models = $state<ModelInfoX[]>([]);
  let cap = $state(5);
  let searchKey = $state('');

  const searches = [
    ['searxng', 'SearXNG', 'Private search service from the compose file'],
    ['brave', 'Brave Search', 'Needs a Brave API key'],
    ['tavily', 'Tavily', 'Needs a Tavily API key'],
    ['none', 'Off', 'No web search tool'],
  ];

  const search = $derived(config.value<string>('search.provider', 'none'));
  const mainRef = $derived(config.value<string[]>('models.main', [])?.[0] ?? '');
  const mainInfo = $derived(models.find((m) => `${m.ref.provider}/${m.ref.model}` === mainRef));
  // A typical reply: ~6K input tokens (half of it cached system prompt and tools) and ~500 output tokens.
  const perReply = $derived(
    mainInfo?.price.known ? (3000 * mainInfo.price.in_per_m + 3000 * mainInfo.price.cache_read_per_m + 500 * mainInfo.price.out_per_m) / 1e6 : 0,
  );
  const replies = $derived(perReply > 0 ? Math.floor((cap * 1e6) / perReply) : 0);

  onMount(() => {
    api.get<ModelInfoX[]>('/api/models').then((m) => (models = m), () => {});
    void config.load().then(() => (cap = config.value<number>('budgets.daily_usd', 5) || 5));
  });

  let capTimer: ReturnType<typeof setTimeout> | undefined;
  function setCap(v: number) {
    cap = v;
    clearTimeout(capTimer);
    capTimer = setTimeout(() => void config.set({ 'budgets.daily_usd': v }, true), 400);
  }

  async function setSearch(v: string) {
    await config.set({ 'search.provider': v }, true);
  }

  async function saveSearchKey() {
    const name = `${search}_api_key`;
    try {
      await api.put(`/api/vault/global/${name}`, { value: searchKey });
      await config.set({ [`search.${name}`]: `secret:${name}` }, true);
      searchKey = '';
      toasts.ok('Search key saved in the vault.');
    } catch (e) {
      toasts.error(e);
    }
  }
</script>

<h1 class="text-2xl font-semibold tracking-tight">Safety and spending</h1>
<p class="mt-2 text-muted">Sensible defaults are already on. Adjust them now or any time later.</p>

<div class="mt-6 rounded-xl border border-line px-4"><SandboxStatus /></div>

<fieldset class="mt-8">
  <legend class="text-sm font-semibold">How cautious?</legend>
  <div class="mt-3"><PresetPicker examples /></div>
</fieldset>

<div class="mt-8">
  <label for="cap" class="text-sm font-semibold">Daily spending cap: <span class="tabular-nums">${cap}</span></label>
  <input id="cap" type="range" min="1" max="50" step="1" class="mt-3 w-full accent-accent" value={cap} oninput={(e) => setCap(Number(e.currentTarget.value))} />
  <p class="mt-1 text-sm text-muted">
    {#if replies}≈ {replies.toLocaleString()} typical replies a day on {mainInfo?.display || mainRef}.{:else if mainInfo?.local}Your main model runs locally and costs nothing.{:else}Bothy stops new runs for the day when the cap is reached.{/if}
  </p>
</div>

<div class="mt-8 grid gap-4 sm:grid-cols-2">
  <div>
    <label class="label" for="on-timeout">If you don't answer an approval in 15 minutes</label>
    <select id="on-timeout" class="input" value={config.value('policy.approval.interactive_on_timeout', 'deny')} onchange={(e) => config.set({ 'policy.approval.interactive_on_timeout': e.currentTarget.value }, true)}>
      <option value="deny">Deny the action</option>
      <option value="park">Park it until you decide (up to a day)</option>
    </select>
  </div>
  <div>
    <span class="label">Package installs from the sandbox</span>
    <label class="mt-2 flex items-center gap-2 text-sm"><input type="checkbox" class="switch" checked={config.flag('egress.presets.python_packages')} onchange={(e) => config.set({ 'egress.presets.python_packages': e.currentTarget.checked }, true)} />Allow PyPI</label>
    <label class="mt-2 flex items-center gap-2 text-sm"><input type="checkbox" class="switch" checked={config.flag('egress.presets.npm_packages')} onchange={(e) => config.set({ 'egress.presets.npm_packages': e.currentTarget.checked }, true)} />Allow npm</label>
  </div>
</div>

<fieldset class="mt-8">
  <legend class="text-sm font-semibold">Web search</legend>
  <div class="mt-3 grid gap-2 sm:grid-cols-2">
    {#each searches as [v, title, text] (v)}
      <label class="card flex cursor-pointer items-start gap-3 p-3 text-sm has-checked:border-accent has-checked:bg-accent-soft/40">
        <input type="radio" name="search" class="mt-1 accent-accent" value={v} checked={search === v} onchange={() => setSearch(v)} />
        <span><span class="font-medium">{title}</span><span class="block text-muted">{text}</span></span>
      </label>
    {/each}
  </div>
  {#if search === 'brave' || search === 'tavily'}
    <form class="mt-3 flex gap-2" onsubmit={(e) => (e.preventDefault(), saveSearchKey())}>
      <input class="input mono" type="password" autocomplete="off" placeholder="{search === 'brave' ? 'Brave' : 'Tavily'} API key" aria-label="Search API key" required bind:value={searchKey} />
      <button class="btn">Save key</button>
    </form>
  {/if}
</fieldset>

<div class="mt-8 flex justify-end">
  <button type="button" class="btn btn-primary" onclick={onnext}>Continue</button>
</div>
