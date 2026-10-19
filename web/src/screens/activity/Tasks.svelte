<script lang="ts">
  import { untrack } from 'svelte';
  import { EmptyState, Select } from '../../components/ui';
  import { api } from '../../lib/api/client';
  import type { List, Schedule } from '../../lib/api/types.gen';
  import { clock as time, fullTime, money } from '../../lib/format';
  import { router } from '../../lib/router.svelte';
  import { session } from '../../lib/stores/session.svelte';
  import { matchesResult, matchesWhere } from '../../lib/taskStatus';
  import Activity from './Activity.svelte';
  import { people, sessionsById, type TaskRun, type VotedApproval } from './api';
  import Loading from '../../components/ui/Loading.svelte';
  import ShowEarlier from './ShowEarlier.svelte';
  import TaskRow from './TaskRow.svelte';
  import { groupByDay, taskView, type TaskLookups } from './tasks';

  let runs = $state<TaskRun[] | null>(null);
  let next = $state(0);
  let failed = $state(false);
  let lookups = $state<TaskLookups>({ names: new Map(), sessions: new Map(), schedules: new Map(), waiting: new Map() });

  const person = $derived(router.query.get('person') ?? '');
  const place = $derived(router.query.get('where') ?? '');
  const result = $derived(router.query.get('result') ?? '');

  async function load() {
    failed = false;
    try {
      const [page, names, sessions, schedules, pending] = await Promise.all([
        api.get<List<TaskRun>>('/api/runs?limit=50'),
        people(),
        sessionsById(),
        api.get<Schedule[]>('/api/schedules').catch(() => [] as Schedule[]),
        api.get<List<VotedApproval>>('/api/approvals?status=pending').catch(() => ({ items: [] as VotedApproval[] })),
      ]);
      lookups = { names, sessions, schedules: new Map(schedules.map((s) => [s.id, s])), waiting: new Map((pending.items ?? []).map((a) => [a.run_id, a])) };
      runs = page.items ?? [];
      next = page.next ?? 0;
    } catch {
      failed = true;
    }
  }

  async function earlier() {
    const page = await api.get<List<TaskRun>>(`/api/runs?limit=50&before=${next}`);
    const seen = new Set(runs?.map((r) => r.id));
    runs = [...(runs ?? []), ...(page.items ?? []).filter((r) => !seen.has(r.id))];
    next = page.next ?? 0;
  }

  $effect(() => {
    untrack(() => void load());
  });

  const shown = $derived(
    (runs ?? []).filter((r) => (!person || r.user_id === person) && matchesWhere(r.origin, r.trigger, place) && matchesResult(r.status, result)),
  );
  const groups = $derived(groupByDay(shown, (r) => r.created_at));
  const filtered = $derived(!!(person || place || result));

  const personOptions = $derived([
    { value: '', label: 'Everyone' },
    ...[...lookups.names].map(([id, name]) => ({ value: id, label: id === session.me?.user.id ? 'You' : name })),
  ]);
  const placeOptions = [
    { value: '', label: 'Anywhere' },
    { value: 'chats', label: 'Chats' },
    { value: 'schedules', label: 'Schedules' },
    { value: 'apps', label: 'Chat apps' },
    { value: 'api', label: 'API' },
  ];
  const resultOptions = [
    { value: '', label: 'Any result' },
    { value: 'attention', label: 'Needs attention' },
    { value: 'failed', label: 'Failed' },
  ];
</script>

<Activity>
  {#if runs === null}
    <Loading {failed} retry={load} />
  {:else if !runs.length}
    <EmptyState text="Nothing yet. Tasks Bothy works on, in chats or on a schedule, show up here." action={{ label: 'Start a chat', href: '/chat' }} />
  {:else}
    <div class="flex flex-wrap gap-2">
      {#if session.isAdmin}
        <Select label="Person" hideLabel options={personOptions} value={person} onchange={(e) => router.setQuery({ person: e.currentTarget.value })} />
      {/if}
      <Select label="Where" hideLabel options={placeOptions} value={place} onchange={(e) => router.setQuery({ where: e.currentTarget.value })} />
      <Select label="Result" hideLabel options={resultOptions} value={result} onchange={(e) => router.setQuery({ result: e.currentTarget.value })} />
    </div>

    {#if !shown.length && filtered}
      <EmptyState class="mt-6" text="Nothing matches these filters." action={{ label: 'Clear filters', variant: 'secondary', onclick: () => router.setQuery({ person: null, where: null, result: null }) }} />
    {/if}

    {#each groups as g (g.label)}
      <section class="mt-6">
        <h2 class="mb-1 text-caption text-muted">{g.label}</h2>
        <ul>
          {#each g.items as r (r.id)}
            <TaskRow
              href="/activity/tasks/{encodeURIComponent(r.id)}"
              view={taskView(r, lookups)}
              cost={r.cost_micros ? money(r.cost_micros) : '—'}
              time={time(r.created_at)}
              fullTime={fullTime(r.created_at)}
            />
          {/each}
        </ul>
      </section>
    {/each}

    {#if next}<ShowEarlier load={earlier} />{/if}
  {/if}
</Activity>
