<script lang="ts">
  import { untrack } from 'svelte';
  import { Button, EmptyState, IconButton, Menu, StatusLine, Switch, type MenuItem } from '../../components/ui';
  import { api, refusal } from '../../lib/api/client';
  import type { List, Schedule, ScheduleExamples, SchedulePreviewResp, ScheduleRunData, TeamSchedule } from '../../lib/api/types.gen';
  import Icon from '../../components/Icon.svelte';
  import { appName } from '../../lib/cardcopy';
  import { router } from '../../lib/router.svelte';
  import { clockLabel, describeWhen, fromSpec, localTimes, localZone, saveSchedule, scheduleGrants, zoneTime } from '../../lib/schedules';
  import { events } from '../../lib/sse.svelte';
  import { session } from '../../lib/stores/session.svelte';
  import Activity from './Activity.svelte';
  import DeleteSchedule from './DeleteSchedule.svelte';
  import type { TaskRun } from './api';
  import Loading from '../../components/ui/Loading.svelte';
  import { checkinBlank, every, nextRun, runNow } from './schedule';
  import { deliverTo } from './tasks';

  let list = $state<Schedule[] | null>(null);
  let ex = $state<ScheduleExamples | null>(null);
  let custom = $state<Record<string, string>>({});
  let problems = $state<Record<string, string>>({});
  let failed = $state(false);
  let errors = $state<Record<string, string>>({});
  let checkinOn = $state(false);
  let removing = $state<Schedule | null>(null);
  // Admins also manage the schedules people set up in chat apps (§10.3).
  let team = $state<TeamSchedule[]>([]);
  let teamFailed = $state(false);
  let removingTeam = $state<TeamSchedule | null>(null);

  async function loadTeam() {
    try {
      team = await api.get<TeamSchedule[]>('/api/team/schedules');
      teamFailed = false;
    } catch {
      teamFailed = true;
    }
  }

  async function load() {
    failed = false;
    try {
      // Together, so the empty sentence never shows before the examples that replace it.
      const [l, x] = await Promise.all([api.get<Schedule[]>('/api/schedules'), api.get<ScheduleExamples>('/api/schedules/examples').catch(() => null)]);
      ex = x;
      list = l;
      if (session.isAdmin) void loadTeam();
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
    // A check-in with an empty list would check nothing, so turning it on opens the list instead.
    if (on && ex?.checkin.empty && (!s || s.kind === 'heartbeat')) return router.navigate('/activity/schedules/checkin?on=1');
    const key = s?.id ?? 'checkin';
    errors[key] = '';
    if (s) s.enabled = on;
    else checkinOn = on;
    try {
      const saved = await saveSchedule({ ...(s ?? checkinBlank(session.me?.user)), enabled: on }, s?.id ? await scheduleGrants(s.id) : []);
      list = [...(list ?? []).filter((x) => x.id !== saved.id), saved];
    } catch (e) {
      if (s) s.enabled = !on;
      else checkinOn = !on;
      errors[key] = refusal(e) || "Couldn't save. Try again.";
    }
  }

  function menu(s: Schedule | undefined, path: string): MenuItem[] {
    const items: MenuItem[] = [
      ...(s ? [{ label: 'Run now', onselect: async () => (errors[s.id] = await runNow(s)) }] : []),
      { label: 'Edit', onselect: () => router.navigate(path) },
      ...(s ? [{ label: 'History', onselect: () => router.navigate(`${path}#recent`) }] : []),
    ];
    if (s && s.kind !== 'heartbeat') items.push('separator', { label: 'Delete…', danger: true, onselect: () => (removing = s) });
    return items;
  }

  async function toggleTeam(t: TeamSchedule, on: boolean) {
    errors[t.id] = '';
    t.enabled = on;
    try {
      const saved = await api.patch<Schedule>(`/api/schedules/${encodeURIComponent(t.id)}`, { enabled: on });
      // The server clears the next run while a schedule is off; Go's zero time means none.
      t.next_run_at = saved.next_run_at && !saved.next_run_at.startsWith('0001-') ? saved.next_run_at : undefined;
    } catch {
      t.enabled = !on;
      errors[t.id] = "Couldn't save. Try again.";
    }
  }

  // Times read in the schedule's own zone, so another zone than the viewer's is named.
  function when(s: Pick<Schedule, 'id' | 'kind' | 'spec'> & { tz?: string }): string {
    const text = describeWhen(fromSpec(s.kind, s.spec, s.tz)) ?? custom[s.id] ?? 'On a custom repeat rule';
    return s.tz && s.tz !== (session.me?.user.timezone || localZone()) ? `${text}, ${zoneTime(s.tz)}` : text;
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

{#snippet newButton()}<Button variant="primary" href="/activity/schedules/new">New schedule</Button>{/snippet}

<Activity tab="Schedules" actions={list ? newButton : undefined}>
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
          { text: `Posts ${deliverTo(s.deliver)}` },
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
    {#if ex?.examples.length}
      <section class="mt-10" aria-labelledby="schedule-examples">
        <h2 id="schedule-examples" class="section-title">Examples</h2>
        <ul class="mt-2">
          {#each ex.examples as e (e.id)}
            <li class="border-b border-border last:border-b-0">
              <!-- One that can't run yet leads to what it needs, so a new install still shows what schedules do. -->
              <a
                href={e.ready ? `/activity/schedules/new?example=${encodeURIComponent(e.id)}` : e.needs === 'github' ? '/settings/connections/github' : '/settings/connections'}
                onclick={router.link}
                class="-mx-2 flex min-h-14 items-center gap-3 rounded-md px-2 py-2 hover:bg-subtle focus-visible:outline-offset-[-2px]"
              >
                <span class="min-w-0 flex-1">
                  <span class="block text-body font-medium text-text">{e.name}</span>
                  <span class="block text-small text-muted"
                    >{when(e)} · {e.ready ? `Uses ${e.needs === 'github' ? 'GitHub' : 'web search'}` : e.needs === 'github' ? 'Connect GitHub first' : 'Turn on web search first'}</span
                  >
                </span>
                <Icon name="chevron-right" class="text-muted" />
              </a>
            </li>
          {/each}
        </ul>
      </section>
    {:else if !others.length}
      <EmptyState class="mt-6" text="Add your own, like a Monday summary of open pull requests." />
    {/if}
    {#if team.length || teamFailed}
      <section class="mt-10" aria-labelledby="team-schedules">
        <h2 id="team-schedules" class="section-title">In chat apps</h2>
        <p class="mt-1 text-small text-muted">Schedules people set up in chat apps. Their results go to that chat.</p>
        {#if teamFailed}
          <StatusLine class="mt-3" tone="danger" text="These schedules didn't load. Check your connection and try again." action={{ label: 'Try again', onclick: loadTeam }} />
        {:else}
          <ul class="mt-2">
            {#each team as t (t.id)}
              <li class="flex min-h-14 items-center gap-3 border-b border-border py-2 last:border-b-0">
                <div class="min-w-0 flex-1">
                  <p class="text-body font-medium text-text [overflow-wrap:anywhere]" data-copy-ignore>{t.name}</p>
                  <p class="text-small text-muted">
                    <span data-copy-ignore>{t.owner}</span> · {appName(t.app)} · {when(t)} · {!t.enabled ? 'Off' : t.next_run_at ? `Next: ${nextRun(t.next_run_at, t.tz)}` : 'Not scheduled'}
                  </p>
                  {#if errors[t.id]}<p class="text-small text-danger-text" role="alert">{errors[t.id]}</p>{/if}
                </div>
                <Switch label={t.name} hideLabel checked={t.enabled} onchange={(on) => toggleTeam(t, on)} />
                <!-- The same ⋯ as the list above, so the switches line up and the meta line keeps its width. -->
                <Menu items={[{ label: 'Delete…', danger: true, onselect: () => (removingTeam = t) }]} placement="bottom-end">
                  {#snippet trigger(props)}<IconButton icon="more" label="More actions for {t.name}" size="sm" {...props} />{/snippet}
                </Menu>
              </li>
            {/each}
          </ul>
        {/if}
      </section>
    {/if}
  {/if}
</Activity>

<DeleteSchedule bind:schedule={removing} ondeleted={(gone) => (list = (list ?? []).filter((x) => x.id !== gone.id))} />
<DeleteSchedule bind:schedule={removingTeam} team ondeleted={(gone) => (team = team.filter((x) => x.id !== gone.id))} />
