<script lang="ts">
  import { untrack } from 'svelte';
  import { Button, EmptyState, IconButton, Menu, Switch, toast, type MenuItem } from '../../components/ui';
  import { api } from '../../lib/api/client';
  import type { List, Schedule, SchedulePreviewResp, ScheduleRunData } from '../../lib/api/types.gen';
  import { router } from '../../lib/router.svelte';
  import { clockLabel, describeWhen, fromSpec, localTimes, saveSchedule, scheduleGrants } from '../../lib/schedules';
  import { events } from '../../lib/sse.svelte';
  import { session } from '../../lib/stores/session.svelte';
  import Activity from './Activity.svelte';
  import DeleteSchedule from './DeleteSchedule.svelte';
  import type { TaskRun } from './api';
  import Loading from './Loading.svelte';
  import { checkinBlank, every, nextRun } from './schedule';
  import { deliverTo, loadChats, type Chats } from './tasks';

  let list = $state<Schedule[] | null>(null);
  let chats = $state<Chats>({ identities: [], spaces: [] });
  let custom = $state<Record<string, string>>({});
  let problems = $state<Record<string, string>>({});
  let failed = $state(false);
  let errors = $state<Record<string, string>>({});
  let checkinOn = $state(false);
  let removing = $state<Schedule | null>(null);

  async function load() {
    failed = false;
    try {
      const [l, c] = await Promise.all([api.get<Schedule[]>('/api/schedules'), loadChats()]);
      chats = c;
      list = l;
      for (const s of l) {
        if (s.kind === 'cron' && fromSpec(s.kind, s.spec, s.tz).kind === 'custom')
          void api.post<SchedulePreviewResp>('/api/schedules/preview', { kind: s.kind, spec: s.spec, tz: s.tz }).then((p) => (custom[s.id] = localTimes(p.human.replace(/ \([^)]*\)$/, ''))), () => {});
        if (s.last_status === 'failed')
          void api.get<List<TaskRun>>(`/api/schedules/${encodeURIComponent(s.id)}/runs?limit=1`).then((r) => (problems[s.id] = r.items?.[0]?.reason ?? ''), () => {});
      }
    } catch {
      failed = true;
    }
  }

  $effect(() => {
    untrack(() => void load());
    return events.on('schedule.run', (e) => {
      const d = e.data as ScheduleRunData;
      const s = list?.find((x) => x.id === d.schedule_id);
      if (s) s.last_status = d.status;
    });
  });

  const checkin = $derived(list?.find((s) => s.kind === 'heartbeat'));
  const others = $derived((list ?? []).filter((s) => s.kind !== 'heartbeat').sort((a, b) => a.name.localeCompare(b.name)));

  // Optimistic, so the switch moves at once and moves back if the save fails.
  async function toggle(s: Schedule | undefined, on: boolean) {
    const key = s?.id ?? 'checkin';
    errors[key] = '';
    if (s) s.enabled = on;
    else checkinOn = on;
    try {
      const saved = await saveSchedule({ ...(s ?? checkinBlank(session.me?.user)), enabled: on }, s?.id ? await scheduleGrants(s.id) : []);
      list = [...(list ?? []).filter((x) => x.id !== saved.id), saved];
    } catch {
      if (s) s.enabled = !on;
      else checkinOn = !on;
      errors[key] = "Couldn't save. Try again.";
    }
  }

  async function runNow(s: Schedule) {
    try {
      const r = await api.post<{ run_id: string }>(`/api/schedules/${encodeURIComponent(s.id)}/run`);
      toast('Schedule started.', r?.run_id ? { action: { label: 'View', onclick: () => router.navigate(`/activity/tasks/${encodeURIComponent(r.run_id)}`) } } : {});
    } catch {
      errors[s.id] = "Couldn't start it. Try again.";
    }
  }

  function menu(s: Schedule | undefined, path: string): MenuItem[] {
    const items: MenuItem[] = [
      ...(s ? [{ label: 'Run now', onselect: () => runNow(s) }] : []),
      { label: 'Edit', onselect: () => router.navigate(path) },
      ...(s ? [{ label: 'History', onselect: () => router.navigate(`${path}#recent`) }] : []),
    ];
    if (s && s.kind !== 'heartbeat') items.push('separator', { label: 'Delete…', danger: true, onselect: () => (removing = s) });
    return items;
  }

  function when(s: Schedule): string {
    return describeWhen(fromSpec(s.kind, s.spec, s.tz)) ?? custom[s.id] ?? 'On a custom repeat rule';
  }

  function hours(s: Schedule | undefined): string {
    const [from, to] = (s?.active_hours || '08:00-22:00').split('-');
    return `${clockLabel(from)}–${clockLabel(to)}`;
  }
</script>

{#snippet row(name: string, href: string, s: Schedule | undefined, enabled: boolean, meta: { text: string; danger?: boolean }[])}
  {@const error = errors[s?.id ?? 'checkin']}
  <li class="flex min-h-14 items-center gap-3 border-b border-border py-2 last:border-b-0">
    <div class="min-w-0 flex-1">
      <a {href} onclick={router.link} class="-my-0.5 flex min-h-6 items-center text-body font-medium text-text hover:underline [overflow-wrap:anywhere] pointer-coarse:-my-3 pointer-coarse:min-h-11">{name}</a>
      <p class="text-small text-muted">
        {#each meta as m, i (i)}{#if i}{' · '}{/if}<span class={m.danger ? 'text-danger-text' : ''}>{m.text}</span>{/each}
      </p>
      {#if error}<p class="text-small text-danger-text" role="alert">{error}</p>{/if}
    </div>
    <Switch label={name} hideLabel checked={enabled} onchange={(on) => toggle(s, on)} />
    <Menu items={menu(s, href)} placement="bottom-end">
      {#snippet trigger(props)}<IconButton icon="more" label="More for {name}" size="sm" {...props} />{/snippet}
    </Menu>
  </li>
{/snippet}

{#snippet newButton()}
  {#if others.length}<Button variant="primary" href="/activity/schedules/new">New schedule</Button>{/if}
{/snippet}

<Activity title="Schedules" actions={list ? newButton : undefined}>
  {#if list === null}
    <Loading {failed} retry={load} />
  {:else}
    <ul>
      {@render row('Check-in', '/activity/schedules/checkin', checkin, checkin ? checkin.enabled : checkinOn, [
        { text: `${every(checkin?.spec ?? '30m')}, ${hours(checkin)}` },
        { text: 'Looks through your list and tells you only when something needs attention' },
      ])}
      {#each others as s (s.id)}
        {@render row(s.name, `/activity/schedules/${encodeURIComponent(s.id)}`, s, s.enabled, [
          { text: when(s) },
          { text: `Posts ${deliverTo(s.deliver, chats)}` },
          ...(s.failures >= 3
            ? [{ text: 'Turned off after 3 failed runs', danger: true }]
            : s.last_status === 'failed'
              ? [{ text: problems[s.id] ? `Last run failed: ${problems[s.id]}` : 'Last run failed', danger: true }]
              : s.enabled && s.next_run_at
                ? [{ text: `Next: ${nextRun(s.next_run_at, s.tz)}` }]
                : []),
        ])}
      {/each}
    </ul>
    {#if !others.length}
      <EmptyState class="mt-6" text="Bothy can do things on a schedule, like a Monday summary of open pull requests." action={{ label: 'New schedule', href: '/activity/schedules/new' }} />
    {/if}
  {/if}
</Activity>

<DeleteSchedule bind:schedule={removing} ondeleted={(gone) => (list = (list ?? []).filter((x) => x.id !== gone.id))} />
