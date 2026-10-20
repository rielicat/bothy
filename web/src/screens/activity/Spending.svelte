<script lang="ts">
  import BarChart, { type Bar } from '../../components/BarChart.svelte';
  import Icon from '../../components/Icon.svelte';
  import { Disclosure, EmptyState, LinkButton, ProgressBar, Segmented } from '../../components/ui';
  import { api, qs } from '../../lib/api/client';
  import type { ModelInfo, Schedule, UsageResp, UsageToday } from '../../lib/api/types.gen';
  import { appName } from '../../lib/cardcopy';
  import { money, serviceName } from '../../lib/format';
  import { friendlyModel, provider, providers } from '../../lib/providers';
  import { router } from '../../lib/router.svelte';
  import { session } from '../../lib/stores/session.svelte';
  import Activity from './Activity.svelte';
  import { people, sessionsById, type NamedUsageRow } from './api';
  import Loading from '../../components/ui/Loading.svelte';

  type Group = 'model' | 'user' | 'schedule' | 'session';
  /** `sub`: muted after the name, a model's provider or the chat app of someone in a chat app or a channel. */
  type Line = { key: string; name: string; amount: string; href?: string; unpriced?: boolean; sub?: string };

  const range = $derived(['today', '7'].includes(router.query.get('range') ?? '') ? router.query.get('range')! : '30');
  const days = $derived(range === 'today' ? 1 : Number(range));

  let byDay = $state<UsageResp | null>(null);
  let groups = $state<Partial<Record<Group, NamedUsageRow[]>>>({});
  let today = $state<UsageToday | null>(null);
  let models = $state<ModelInfo[]>([]);
  let labels = $state<Record<string, Map<string, string>>>({});
  let failed = $state(false);
  let open = $state<Record<string, boolean>>({});

  // Day rows are keyed by UTC date (SPEC §8.3), so the range starts at a UTC
  // midnight and every bar is one UTC day.
  const DAY = 86_400_000;
  const utcDay = (back: number) => {
    const n = new Date();
    return new Date(Date.UTC(n.getUTCFullYear(), n.getUTCMonth(), n.getUTCDate()) - back * DAY);
  };

  async function load() {
    failed = false;
    const base = { from: utcDay(days - 1).toISOString(), to: new Date().toISOString() };
    const kinds: Group[] = ['model', 'schedule', 'session', ...(session.isAdmin ? (['user'] as Group[]) : [])];
    try {
      const [d, t, m, names, sessions, schedules, ...rest] = await Promise.all([
        api.get<UsageResp>(`/api/usage${qs({ ...base, group_by: 'day' })}`),
        api.get<UsageToday>('/api/usage/today').catch(() => null),
        api.get<ModelInfo[]>('/api/models').catch(() => [] as ModelInfo[]),
        people(),
        sessionsById(),
        api.get<Schedule[]>('/api/schedules').catch(() => [] as Schedule[]),
        ...kinds.map((k) => api.get<UsageResp>(`/api/usage${qs({ ...base, group_by: k })}`)),
      ]);
      byDay = d;
      today = t;
      models = m;
      labels = { user: names, session: new Map([...sessions].map(([id, s]) => [id, s.title])), schedule: new Map(schedules.map((s) => [s.id, s.name])) };
      groups = Object.fromEntries(kinds.map((k, i) => [k, [...(rest[i].rows ?? [])].sort((a, b) => b.cost_micros - a.cost_micros)]));
    } catch {
      failed = true;
    }
  }

  $effect(() => {
    void days;
    byDay = null;
    void load();
  });

  const total = $derived(byDay?.totals.cost_micros ?? 0);
  const model = (key: string) => models.find((m) => `${m.ref.provider}/${m.ref.model}` === key);

  function lines(g: Group): Line[] {
    return (groups[g] ?? []).map((r) => {
      if (g === 'model') {
        // Model rows carry no name, and a model replaced since is no longer listed; its key stays in Technical details.
        const [providerId, ...rest] = r.key.split('/');
        const info = model(r.key);
        // An id the built-in list doesn't know names its own provider (a removed or custom one), not "Other server".
        const known = providers.some((p) => p.id === providerId);
        return {
          key: r.key,
          name: info?.display_name || friendlyModel(rest.join('/')),
          sub: info?.provider_name || (known ? provider(providerId).name : serviceName(providerId)),
          amount: money(r.cost_micros, { local: info?.local, known: r.price_known }),
          unpriced: !r.price_known && !info?.local,
        };
      }
      const name = r.name ?? labels[g]?.get(r.key) ?? (g === 'session' ? 'A chat' : g === 'schedule' ? 'A schedule' : 'Someone');
      // People in chat apps and channels have no tasks or chats on the web to open.
      if (g === 'user' && (r.kind === 'teammate' || r.kind === 'channel')) return { key: r.key, name, amount: money(r.cost_micros), sub: appName(r.app) };
      const href = g === 'user' ? `/activity?person=${encodeURIComponent(r.key)}` : g === 'session' ? `/chat/${encodeURIComponent(r.key)}` : `/activity/schedules/${encodeURIComponent(r.key)}`;
      return { key: r.key, name: g === 'user' && r.key === session.me?.user.id ? 'You' : name, amount: money(r.cost_micros), href };
    });
  }

  const sections = $derived(
    (
      [
        ['user', 'By person'],
        ['model', 'By model'],
        ['schedule', 'By schedule'],
        ['session', 'Top chats'],
      ] as [Group, string][]
    ).filter(([g]) => groups[g]?.length),
  );

  const dayName = (d: Date, o: Intl.DateTimeFormatOptions) => d.toLocaleDateString(undefined, { ...o, timeZone: 'UTC' });
  const bars = $derived.by<Bar[]>(() => {
    const rows = new Map((byDay?.rows ?? []).map((r) => [r.key.slice(0, 10), r.cost_micros]));
    return Array.from({ length: days }, (_, i) => {
      const d = utcDay(days - 1 - i);
      const value = rows.get(d.toISOString().slice(0, 10)) ?? 0;
      const label = i === 0 || i === days - 1 || i === Math.floor(days / 2) ? dayName(d, { day: 'numeric', month: 'short' }) : '';
      const short = days <= 7 ? dayName(d, { weekday: 'narrow' }) : [1, 15].includes(d.getUTCDate()) ? dayName(d, { day: 'numeric', month: 'short' }) : '';
      return { label, short, value, title: `${dayName(d, { weekday: 'short', day: 'numeric', month: 'short' })}: ${money(value)}` };
    });
  });
  const summary = $derived.by(() => {
    if (!bars.length) return '';
    const top = bars.reduce((best, b, i) => (b.value > bars[best].value ? i : best), 0);
    const long = (back: number) => dayName(utcDay(back), { day: 'numeric', month: 'long' });
    return `Spending by day, ${long(days - 1)} to ${long(0)}, highest ${money(bars[top].value)} on ${long(days - 1 - top)}`;
  });
  const axis = (v: number) => money(v).replace(/\.00$/, '');

  const spent = $derived(session.isAdmin ? (today?.global_spent_micros ?? 0) : (today?.spent_micros ?? 0));
  const limit = $derived(session.isAdmin ? (today?.global_limit_micros ?? 0) : (today?.limit_micros ?? 0));
  const ratio = $derived(limit ? spent / limit : 0);
  const tone = $derived(ratio >= 1 ? 'danger' : ratio >= 0.8 ? 'warning' : 'default');
  // Chat apps' share of the daily limit (admins): a line here, not a banner over every page (§4.9).
  const chatSpent = $derived(today?.chat_spent_micros ?? 0);
  const chatLimit = $derived(session.isAdmin ? (today?.chat_limit_micros ?? 0) : 0);
  const chatRatio = $derived(chatLimit ? chatSpent / chatLimit : 0);

  const tokens = (n: number) => n.toLocaleString();
</script>

{#snippet rangePicker()}
  <!-- On touch its 44 px options make it 50 px tall, taller than the header's button strut, which moved the title and tabs 3 px down on this tab only. -->
  <Segmented
    class="pointer-coarse:-my-0.75"
    label="Period"
    options={[
      { value: 'today', label: 'Today' },
      { value: '7', label: '7 days' },
      { value: '30', label: '30 days' },
    ]}
    value={range}
    onchange={(v) => router.setQuery({ range: v === '30' ? null : v })}
  />
{/snippet}

{#snippet setPrice()}
  {#if session.isAdmin}<a href="/settings/advanced" onclick={router.link} class="link inline-flex min-h-6 items-center text-small pointer-coarse:min-h-11">Set price</a>{/if}
{/snippet}

<Activity tab="Spending" actions={rangePicker}>
  {#if !byDay}
    <Loading {failed} retry={load} />
  {:else if !total && !sections.length}
    <EmptyState text="Nothing spent in this period." />
  {:else}
    <div class="list-card">
      <div class="py-4 {days > 1 ? 'border-b border-border' : ''}">
        <p><span class="text-figure text-text tabular-nums">{money(total)}</span> <span class="text-body text-muted">{range === 'today' ? 'today' : `in the last ${days} days`}</span></p>
        {#if today}
          {#if range !== 'today'}
            <p class="mt-1 text-body text-muted tabular-nums">Today: {money(spent)}{limit ? ` of the ${money(limit)} daily limit` : ''}</p>
          {:else if limit}
            <p class="mt-1 text-body text-muted tabular-nums">The daily limit is {money(limit)}.</p>
          {/if}
          {#if limit}
            <ProgressBar class="mt-3" value={spent} max={limit} {tone} label="Today's spending" valueText="{money(spent)} of {money(limit)}" />
            {#if tone === 'danger'}
              <p class="mt-2 text-body text-danger-text">
                Bothy reached today's limit. New work waits until midnight.{#if session.isAdmin}{' '}<LinkButton onclick={() => router.navigate('/settings/general')}>Change limit</LinkButton>{/if}
              </p>
            {:else if tone === 'warning'}
              <p class="mt-2 text-body text-warning-text">Bothy will stop starting new work at {money(limit)}.</p>
            {/if}
          {/if}
          {#if chatLimit}
            {#if chatRatio >= 1}
              <p class="mt-2 text-body text-danger-text tabular-nums">
                Chat apps used their {money(chatLimit)} share of today's limit. Their new work waits until midnight.{' '}<LinkButton
                  onclick={() => router.navigate('/settings/people')}>Change share</LinkButton
                >
              </p>
            {:else if chatRatio >= 0.8}
              <p class="mt-2 text-body text-warning-text tabular-nums">Chat apps have used {Math.floor(chatRatio * 100)}% of their {money(chatLimit)} share of today's limit.</p>
            {:else}
              <p class="mt-2 text-body text-muted tabular-nums">Chat apps today: {money(chatSpent)} of their {money(chatLimit)} share</p>
            {/if}
          {/if}
        {/if}
      </div>
      {#if days > 1}<div class="py-4"><BarChart data={bars} format={(v) => money(v)} {axis} {summary} /></div>{/if}
    </div>

    {#each sections as [g, title] (g)}
      {@const all = lines(g)}
      {@const linked = all.some((l) => l.href)}
      <section class="mt-10">
        <h2 class="section-title">{title}</h2>
        <ul class="list-card mt-2 overflow-hidden">
          {#each open[g] ? all : all.slice(0, 5) as l (l.key)}
            <li class="flex min-h-10 items-center gap-4 border-b border-border last:border-b-0 pointer-coarse:min-h-12">
              {#if l.href}
                <a href={l.href} onclick={router.link} class="row-link flex min-h-10 min-w-0 flex-1 items-center gap-4 pointer-coarse:min-h-12"
                  ><span class="min-w-0 flex-1 truncate text-body text-text" title={l.name}>{l.name}</span><span class="meta">{l.amount}</span><Icon
                    name="chevron-right"
                    class="-ml-1 shrink-0 text-muted"
                  /></a
                >
              {:else}
                <span class="min-w-0 flex-1 truncate text-body text-text" title={l.name}
                  ><span data-copy-ignore>{l.name}</span>{#if l.sub}<span class="text-muted">{' · '}{l.sub}</span>{/if}</span
                >
                {#if l.unpriced}<span class="text-small text-muted">Price not set</span>{@render setPrice()}{:else}<span class="meta">{l.amount}</span>{/if}
                <!-- Room for the › of the rows that open something, so the amounts line up. -->
                {#if linked}<span class="-ml-1 w-4 shrink-0" aria-hidden="true"></span>{/if}
              {/if}
            </li>
          {/each}
        </ul>
        {#if all.length > 5}<LinkButton class="mt-2 text-body" onclick={() => (open[g] = !open[g])}>{open[g] ? 'Show fewer' : 'Show all'}</LinkButton>{/if}
      </section>
    {/each}

    <Disclosure label="Technical details" class="mt-10">
      <table class="w-full text-small">
        <thead>
          <tr class="border-b border-border text-left text-muted">
            <th scope="col" class="py-1.5 pr-3 font-normal">Model</th>
            <th scope="col" class="py-1.5 pr-3 text-right font-normal">Tokens in</th>
            <th scope="col" class="py-1.5 pr-3 text-right font-normal">Out</th>
            <th scope="col" class="py-1.5 pr-3 text-right font-normal">Cached</th>
            <th scope="col" class="py-1.5 text-right font-normal">Calls</th>
          </tr>
        </thead>
        <tbody class="tabular-nums">
          {#each groups.model ?? [] as r (r.key)}
            {@const all = r.input + r.cache_read + r.cache_write}
            <tr class="border-b border-border last:border-b-0">
              <td class="py-1.5 pr-3 [overflow-wrap:anywhere]">{r.key}</td>
              <td class="py-1.5 pr-3 text-right">{tokens(all)}</td>
              <td class="py-1.5 pr-3 text-right">{tokens(r.output)}</td>
              <td class="py-1.5 pr-3 text-right">{all ? Math.round((r.cache_read / all) * 100) : 0}%</td>
              <td class="py-1.5 text-right">{tokens(r.calls)}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    </Disclosure>
  {/if}
</Activity>
