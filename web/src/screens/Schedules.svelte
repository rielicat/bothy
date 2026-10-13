<script lang="ts">
  import Empty from '../components/Empty.svelte';
  import Icon from '../components/Icon.svelte';
  import { api, errorText } from '../lib/api/client';
  import type { List, Run, Schedule, SchedulePreviewResp, ScheduleRunData } from '../lib/api/types.gen';
  import { ago, usd } from '../lib/format';
  import { statusOf } from '../lib/labels';
  import { router } from '../lib/router.svelte';
  import { saveSchedule, scheduleGrants } from '../lib/schedules';
  import { events } from '../lib/sse.svelte';
  import { clock } from '../lib/stores/clock.svelte';
  import { toasts } from '../lib/stores/ui.svelte';

  let list = $state<Schedule[] | null>(null);
  let human = $state<Record<string, string>>({});
  let last = $state<Record<string, Run | undefined>>({});
  let error = $state('');

  async function load() {
    try {
      const l = await api.get<Schedule[]>('/api/schedules');
      list = l;
      void Promise.all(
        l.map(async (s) => {
          const [p, r] = await Promise.all([
            api.post<SchedulePreviewResp>('/api/schedules/preview', { kind: s.kind, spec: s.spec, tz: s.tz }).catch(() => null),
            api.get<List<Run>>(`/api/schedules/${encodeURIComponent(s.id)}/runs?limit=1`).catch(() => null),
          ]);
          if (p) human[s.id] = p.human;
          last[s.id] = r?.items?.[0];
        }),
      );
    } catch (e) {
      error = errorText(e);
    }
  }

  $effect(() => {
    void load();
    const offs = [
      events.on('schedule.run', (e) => {
        const d = e.data as ScheduleRunData;
        const s = list?.find((x) => x.id === d.schedule_id);
        if (s) s.last_status = d.status;
      }),
      events.onResync(() => void load()),
    ];
    return () => offs.forEach((f) => f());
  });

  async function toggle(s: Schedule) {
    try {
      const updated = await saveSchedule({ ...s, enabled: !s.enabled }, await scheduleGrants(s.id));
      Object.assign(s, updated);
    } catch (e) {
      toasts.error(e);
    }
  }

  async function runNow(s: Schedule) {
    try {
      const r = await api.post<{ run_id: string }>(`/api/schedules/${encodeURIComponent(s.id)}/run`);
      toasts.ok(`Started “${s.name}”.`);
      if (r?.run_id) router.navigate(`/runs/${encodeURIComponent(r.run_id)}`);
    } catch (e) {
      toasts.error(e);
    }
  }
</script>

<svelte:head><title>Schedules · Bothy</title></svelte:head>

<div class="page">
  <div class="mb-6 flex flex-wrap items-end gap-3">
    <div class="flex-1">
      <h1 class="page-title">Schedules</h1>
      <p class="mt-1 text-sm text-muted">Tasks Bothy runs on its own. Anything risky parks until you approve it.</p>
    </div>
    <a href="/schedules/new" onclick={router.link} class="btn btn-primary"><Icon name="plus" size={16} />New schedule</a>
  </div>

  {#if error}<p class="alert-bad mb-4" role="alert">{error}</p>{/if}

  {#if list && !list.length}
    <Empty icon="clock" title="No schedules yet">
      A daily briefing, a weekly report, a reminder: tell Bothy in a chat or create one here.
      {#snippet action()}<a href="/schedules/new" onclick={router.link} class="btn btn-primary">Create a schedule</a>{/snippet}
    </Empty>
  {:else if list}
    <ul class="grid gap-3">
      {#each list as s (s.id)}
        {@const st = statusOf(s.last_status)}
        <li class="card flex flex-wrap items-center gap-x-4 gap-y-2 p-4 {s.enabled ? '' : 'opacity-70'}">
          <input type="checkbox" class="switch" checked={s.enabled} aria-label="{s.enabled ? 'Disable' : 'Enable'} {s.name}" onchange={() => toggle(s)} />
          <div class="min-w-0 flex-1">
            <a href="/schedules/{encodeURIComponent(s.id)}" onclick={router.link} class="font-medium hover:underline">{s.name}</a>
            {#if s.kind === 'heartbeat'}<span class="chip ml-1">heartbeat</span>{/if}
            {#if s.failures >= 3}<span class="chip chip-bad ml-1">disabled after 3 failures</span>{/if}
            <p class="mt-0.5 text-sm text-muted">{human[s.id] ?? `${s.spec} (${s.tz})`}</p>
          </div>
          <div class="text-right text-xs text-muted">
            <p>Next: <span class="text-fg">{s.enabled ? ago(s.next_run_at, clock.now) : 'paused'}</span></p>
            <p class="mt-0.5">
              Last: {#if s.last_status}<span class={st.cls}>{st.label}</span>{:else}never{/if}
              {#if last[s.id]}· {usd(last[s.id]!.cost_micros)}{/if}
            </p>
          </div>
          <button type="button" class="btn btn-sm" onclick={() => runNow(s)}><Icon name="play" size={14} />Run now</button>
        </li>
      {/each}
    </ul>
  {:else}
    <div class="space-y-3" aria-busy="true"><div class="skeleton h-20"></div><div class="skeleton h-20"></div></div>
  {/if}
</div>
