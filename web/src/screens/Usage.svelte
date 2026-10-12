<script lang="ts">
  import BarChart from '../components/BarChart.svelte';
  import Empty from '../components/Empty.svelte';
  import { api, errorText, qs } from '../lib/api/client';
  import type { UsageResp, UsageRow } from '../lib/api/types.gen';
  import { tokens, usd } from '../lib/format';
  import { router } from '../lib/router.svelte';
  import { session } from '../lib/stores/session.svelte';

  const ranges = [
    { v: '1', label: 'Today' },
    { v: '7', label: '7 days' },
    { v: '30', label: '30 days' },
  ];
  const range = $derived(ranges.find((r) => r.v === router.query.get('range'))?.v ?? '7');

  let byDay = $state<UsageResp | null>(null);
  let groups = $state<Record<string, UsageRow[]>>({});
  let error = $state('');

  const days = $derived(Number(range));
  const series = $derived.by(() => {
    const rows = new Map((byDay?.rows ?? []).map((r) => [r.key.slice(0, 10), r]));
    const out = [];
    for (let i = days - 1; i >= 0; i--) {
      const d = new Date();
      d.setDate(d.getDate() - i);
      const key = `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`;
      const r = rows.get(key);
      out.push({ key, label: d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' }), value: r?.cost_micros ?? 0, muted: r ? !r.price_known : false });
    }
    return out;
  });
  const t = $derived(byDay?.totals);
  const cacheable = $derived(t ? t.input + t.cache_read + t.cache_write : 0);

  $effect(() => {
    const from = new Date();
    from.setHours(0, 0, 0, 0);
    from.setDate(from.getDate() - (days - 1));
    const base = { from: from.toISOString(), to: new Date().toISOString() };
    const kinds = ['model', 'session', 'schedule', ...(session.isAdmin ? ['user'] : [])];
    error = '';
    Promise.all([
      api.get<UsageResp>(`/api/usage${qs({ ...base, group_by: 'day' })}`),
      ...kinds.map((k) => api.get<UsageResp>(`/api/usage${qs({ ...base, group_by: k })}`)),
    ])
      .then(([d, ...rest]) => {
        byDay = d;
        groups = Object.fromEntries(kinds.map((k, i) => [k, [...(rest[i].rows ?? [])].sort((a, b) => b.cost_micros - a.cost_micros)]));
      })
      .catch((e) => (error = errorText(e)));
  });

  const sections = $derived(
    [
      { k: 'model', title: 'By model' },
      { k: 'user', title: 'By person' },
      { k: 'schedule', title: 'By schedule' },
      { k: 'session', title: 'By chat' },
    ].filter((s) => groups[s.k]?.length),
  );

  function href(kind: string, key: string): string {
    if (kind === 'session') return `/chat/${encodeURIComponent(key)}`;
    if (kind === 'schedule' && key) return `/schedules/${encodeURIComponent(key)}`;
    return '';
  }
</script>

<svelte:head><title>Usage · Bothy</title></svelte:head>

<div class="page">
  <div class="mb-6 flex flex-wrap items-end gap-3">
    <div class="flex-1">
      <h1 class="page-title">Usage</h1>
      <p class="mt-1 text-sm text-muted">What Bothy spent on model calls, from your own API keys.</p>
    </div>
    <div class="flex rounded-lg border border-line bg-surface p-0.5" role="group" aria-label="Range">
      {#each ranges as r (r.v)}
        <button
          type="button"
          class="rounded-md px-3 py-1 text-sm text-muted aria-pressed:bg-sunken aria-pressed:font-medium aria-pressed:text-fg"
          aria-pressed={range === r.v}
          onclick={() => router.setQuery({ range: r.v === '7' ? null : r.v })}>{r.label}</button
        >
      {/each}
    </div>
  </div>

  {#if error}<p class="alert-bad mb-4" role="alert">{error}</p>{/if}

  <dl class="mb-6 grid grid-cols-2 gap-3 lg:grid-cols-4">
    <div class="stat">
      <dt>Spent</dt>
      <dd>{t ? usd(t.cost_micros) : '—'}{#if t && !t.price_known}<span class="ml-1 align-middle text-xs font-normal text-warn">+ unknown</span>{/if}</dd>
    </div>
    <div class="stat"><dt>Today vs cap</dt><dd>{usd(session.today?.spent_micros)}<span class="text-sm font-normal text-muted"> / {session.today?.limit_micros ? usd(session.today.limit_micros) : 'no cap'}</span></dd></div>
    <div class="stat"><dt>Tokens in · out</dt><dd>{t ? `${tokens(cacheable)} · ${tokens(t.output)}` : '—'}</dd></div>
    <div class="stat"><dt>Cache hits</dt><dd>{cacheable ? Math.round(((t?.cache_read ?? 0) / cacheable) * 100) : 0}%</dd></div>
  </dl>

  {#if session.isAdmin && session.today?.global_limit_micros}
    <p class="mb-6 text-sm text-muted">Everyone today: {usd(session.today.global_spent_micros)} of the {usd(session.today.global_limit_micros)} global cap.</p>
  {/if}

  <section class="card mb-8 p-4">
    <h2 class="mb-2 section-title">Spend by day</h2>
    <BarChart data={series} label="Spend per day" format={(v) => usd(v)} />
    {#if series.some((s) => s.muted)}<p class="mt-1 text-xs text-warn">Amber days include calls whose price is unknown.</p>{/if}
  </section>

  {#if !sections.length && byDay}
    <Empty icon="chart" title="No usage in this range">Send a message and the cost of each model call shows up here.</Empty>
  {/if}

  <div class="grid gap-6 lg:grid-cols-2">
    {#each sections as s (s.k)}
      <section class="card overflow-hidden">
        <h2 class="border-b border-line px-4 py-3 section-title">{s.title}</h2>
        <div class="overflow-x-auto">
          <table class="table">
            <thead><tr><th>Name</th><th class="text-right">Cost</th><th class="text-right">Calls</th><th class="text-right">Tokens</th></tr></thead>
            <tbody>
              {#each groups[s.k].slice(0, 12) as r (r.key)}
                {@const link = href(s.k, r.key)}
                <tr>
                  <td class="max-w-56 truncate">
                    {#if link}<a class="link" href={link} onclick={router.link}>{r.key}</a>{:else}{r.key || 'interactive'}{/if}
                  </td>
                  <td class="text-right tabular-nums">{usd(r.cost_micros)}{#if !r.price_known}<span class="ml-1 text-xs text-warn" title="price unknown">?</span>{/if}</td>
                  <td class="text-right tabular-nums">{r.calls}</td>
                  <td class="text-right tabular-nums">{tokens(r.input + r.cache_read + r.cache_write + r.output)}</td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      </section>
    {/each}
  </div>
</div>
