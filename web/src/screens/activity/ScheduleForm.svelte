<script lang="ts">
  import { tick, untrack } from 'svelte';
  import { Button, Checkbox, Combobox, Disclosure, IconButton, Menu, Page, Select, TextArea, TextField, toast } from '../../components/ui';
  import { api, ApiError, refusal } from '../../lib/api/client';
  import type { Grant, List, Schedule, SchedulePreviewResp } from '../../lib/api/types.gen';
  import { fullTime, money } from '../../lib/format';
  import { hostPort } from '../../lib/grants';
  import { router } from '../../lib/router.svelte';
  import { describeWhen, fromSpec, localTimes, saveSchedule, scheduleGrants, timeOptions, toSpec, validRule, wallClock, WEEKDAYS, ZERO, zoneOptions, zoneTime, type When } from '../../lib/schedules';
  import { session } from '../../lib/stores/session.svelte';
  import { people, type TaskRun } from './api';
  import CheckIn from './CheckIn.svelte';
  import DeleteSchedule from './DeleteSchedule.svelte';
  import Loading from '../../components/ui/Loading.svelte';
  import { blankSchedule, grantSentence, hostsIn, listDate, nameFrom } from './schedule';
  import TaskRow from './TaskRow.svelte';
  import { dayLabel, taskView } from './tasks';

  let { params }: { params: Record<string, string> } = $props();

  const isNew = $derived(params.id === 'new');
  let s = $state<Schedule | null>(null);
  let w = $state<When>(fromSpec('cron', '0 9 * * 1-5'));
  let saved = $state<Grant[]>([]);
  let keep = $state<Record<string, boolean>>({});
  let runs = $state<TaskRun[]>([]);
  let names = $state(new Map<string, string>());
  let cost = $state('');
  let from = $state('');
  let to = $state('20:00');
  let named = $state(false);
  let submitted = $state(false);
  let saving = $state(false);
  let saveError = $state('');
  // Keyed by what was asked, so a new choice never shows the previous choice's dates.
  let preview = $state<{ key: string; resp: SchedulePreviewResp } | null>(null);
  let rejected = $state('');
  let missing = $state(false);
  let failed = $state(false);
  let removing = $state<Schedule | null>(null);

  // By city ("London (Europe)"); the IANA id stays the stored value.
  const zones = $derived(zoneOptions(s?.tz ?? ''));

  async function load() {
    const id = params.id;
    failed = missing = false;
    try {
      if (id === 'new') {
        s = { ...blankSchedule(session.me?.user), deliver: { channel: 'web', chat_id: '', kind: 'dm' } };
        saved = [];
        runs = [];
      } else {
        const [sc, gr, rn, n] = await Promise.all([
          api.get<Schedule>(`/api/schedules/${encodeURIComponent(id)}`),
          scheduleGrants(id),
          api.get<List<TaskRun>>(`/api/schedules/${encodeURIComponent(id)}/runs?limit=10`).catch(() => ({ items: [] as TaskRun[] })),
          people(),
        ]);
        s = sc;
        saved = gr;
        runs = rn.items ?? [];
        names = n;
        named = true;
      }
      w = fromSpec(s.kind, s.spec, s.tz);
      [from, to] = s.active_hours ? s.active_hours.split('-') : ['', '20:00'];
      cost = s.limits.max_cost_micros ? (s.limits.max_cost_micros / 1e6).toFixed(2) : '';
      keep = Object.fromEntries(saved.map((g) => [g.id, true]));
    } catch (e) {
      if (e instanceof ApiError && e.status === 404) missing = true;
      else failed = true;
    }
  }

  $effect(() => {
    void params.id;
    s = null;
    untrack(() => void load());
  });

  const spec = $derived(toSpec(w));

  // Custom starts from the rule the last choice made, so it is a small edit rather than a blank field.
  let lastCron = '';
  $effect(() => {
    if (w.kind !== 'custom' && w.kind !== 'once') lastCron = spec.spec;
    else if (w.kind === 'custom' && !w.rule) w.rule = lastCron;
  });

  // "History" in the schedule list opens this page at its recent runs.
  $effect(() => {
    if (s?.id && router.hash === '#recent') void tick().then(() => document.getElementById('recent')?.scrollIntoView());
  });

  const previewKey = $derived(`${spec.kind} ${spec.spec} ${s?.tz ?? ''}`);
  const shown = $derived(preview?.key === previewKey ? preview.resp : null);
  const ruleRejected = $derived(rejected !== '' && rejected === previewKey);
  $effect(() => {
    const key = previewKey;
    const req = { kind: spec.kind, spec: spec.spec, tz: s?.tz ?? 'UTC' };
    if (!s || (w.kind === 'custom' && !validRule(w.rule)) || (w.kind === 'once' && !w.date)) return;
    const t = setTimeout(() => {
      api.post<SchedulePreviewResp>('/api/schedules/preview', req).then(
        (resp) => (preview = { key, resp }),
        () => (rejected = w.kind === 'custom' ? key : ''),
      );
    }, 300);
    return () => clearTimeout(t);
  });

  // Weekday names keep their capital: "Runs Mondays at 9:00", "Runs weekdays at 9:00".
  const lower = (t: string) => (w.kind === 'weekly' ? t : t[0].toLowerCase() + t.slice(1));
  const nextDays = $derived((shown?.next ?? []).slice(0, 3).map((t) => listDate(t, s?.tz)));
  // Accounts have no chat app, so results always stay here: the preview says so instead of a field with nothing to choose.
  const previewLine = $derived.by(() => {
    if (!s) return '';
    const plain = describeWhen(w);
    const when = plain ? lower(plain) : shown ? lower(localTimes(shown.human)) : '';
    const runsText = when ? `Runs ${when} ${zoneTime(s.tz)} and posts the result here.` : '';
    return runsText && w.kind !== 'once' && nextDays.length ? `${runsText} Next: ${nextDays.join(', ')}.` : runsText;
  });
  const today = $derived(s ? wallClock(new Date(), s.tz).slice(0, 10) : undefined);

  const promptError = $derived(submitted && !s?.prompt.trim() ? 'Tell Bothy what to do.' : '');
  const ruleError = $derived(
    w.kind === 'custom' && ((submitted && !validRule(w.rule)) || ruleRejected) ? "That repeat rule isn't valid. Example: 0 9 * * 1-5 means weekdays at 9:00." : '',
  );

  const suggested = $derived.by(() => {
    if (!s) return [] as Grant[];
    const out: Grant[] = [];
    const add = (g: Pick<Grant, 'kind' | 'value' | 'max_se'>) => {
      if (![...saved, ...out].some((x) => x.kind === g.kind && x.value === g.value))
        out.push({ ...g, id: `new:${g.kind}:${g.value}`, principal_id: s!.principal_id, scope: 'schedule', scope_id: s!.id, allow_when_tainted: false, uses: 0, max_uses: 0, created_by: '', created_at: ZERO, expires_at: ZERO });
    };
    for (const h of hostsIn(s.prompt)) add({ kind: 'domain', value: hostPort(h), max_se: 'read' });
    return out;
  });
  // Websites Bothy would read start unticked.
  const kept = (g: Grant) => keep[g.id] ?? false;

  async function save(e: SubmitEvent) {
    e.preventDefault();
    submitted = true;
    if (!s || promptError || ruleError) {
      await tick();
      document.querySelector<HTMLElement>('form [aria-invalid="true"]')?.focus();
      return;
    }
    saving = true;
    saveError = '';
    const out: Schedule = {
      ...$state.snapshot(s),
      kind: spec.kind,
      spec: spec.spec,
      name: s.name.trim() || nameFrom(s.prompt),
      active_hours: from && to ? `${from}-${to}` : '',
      limits: { ...s.limits, max_cost_micros: Math.round((Number.parseFloat(cost) || 0) * 1e6) },
    };
    const grants = [...saved, ...suggested].filter(kept).map((g) => ({ ...g, id: g.id.startsWith('new:') ? '' : g.id }));
    try {
      await saveSchedule(out, grants);
      toast(isNew ? 'Schedule created.' : 'Saved.');
      router.navigate('/activity/schedules');
    } catch (e) {
      // A time already past or outside the chosen hours: retrying can't help, so say why.
      saveError = refusal(e) || "Couldn't save. Try again.";
    } finally {
      saving = false;
    }
  }

  const whenOptions = [
    { value: 'daily', label: 'Every day' },
    { value: 'weekdays', label: 'Every weekday' },
    { value: 'weekly', label: 'Every week on' },
    { value: 'monthly', label: 'Every month on day' },
    { value: 'once', label: 'Once on' },
    { value: 'custom', label: 'Custom' },
  ];
  const runTime = (t: string) => `${dayLabel(t)} at ${new Date(t).toLocaleTimeString(undefined, { hour: 'numeric', minute: '2-digit' })}`;
  const dayOptions = WEEKDAYS.map((d, i) => ({ value: String(i), label: d }));
  const dateOptions = Array.from({ length: 31 }, (_, i) => ({ value: String(i + 1), label: String(i + 1) }));
</script>

{#snippet pageMenu()}
  <Menu items={[{ label: 'Delete schedule…', danger: true, onselect: () => (removing = s) }]} placement="bottom-end">
    {#snippet trigger(props)}<IconButton icon="more" label="More for this schedule" {...props} />{/snippet}
  </Menu>
{/snippet}

{#if params.id === 'checkin'}
  <CheckIn />
{:else if missing}
  <Page title="Schedule not found" back={{ href: '/activity/schedules', label: 'Schedules' }}>
    <p class="text-reading text-text">This schedule doesn't exist or was deleted.</p>
    <Button class="mt-4" variant="primary" href="/activity/schedules">Go to Schedules</Button>
  </Page>
{:else if !s}
  <Page title={isNew ? 'New schedule' : 'Schedule'} back={{ href: '/activity/schedules', label: 'Schedules' }}><Loading {failed} retry={load} /></Page>
{:else}
  <Page title={isNew ? 'New schedule' : s.name} back={{ href: '/activity/schedules', label: 'Schedules' }} actions={isNew ? undefined : pageMenu}>
    <form novalidate onsubmit={save}>
      <TextArea
        label="What should Bothy do?"
        bind:value={s.prompt}
        rows={3}
        maxRows={12}
        error={promptError}
        placeholder="Summarise yesterday's merged pull requests and post them to the team."
        oninput={() => !named && s && (s.name = nameFrom(s.prompt))}
      />

      <fieldset class="mt-6">
        <legend class="field-label">When</legend>
        <div class="mt-2 flex flex-wrap items-center gap-2">
          <Select label="Repeat" hideLabel options={whenOptions} bind:value={w.kind} />
          {#if w.kind === 'weekly'}
            <Select label="Day of the week" hideLabel options={dayOptions} value={String(w.day)} onchange={(e) => (w.day = Number(e.currentTarget.value))} />
          {:else if w.kind === 'monthly'}
            <Select label="Day of the month" hideLabel options={dateOptions} value={String(w.day)} onchange={(e) => (w.day = Number(e.currentTarget.value))} />
          {:else if w.kind === 'once'}
            <input type="date" class="input w-auto" aria-label="Date" min={today} bind:value={w.date} />
          {/if}
          {#if w.kind !== 'custom'}
            <span class="flex items-center gap-2">
              <span class="text-body text-text">at</span>
              <Select label="Time" hideLabel options={timeOptions(w.time)} bind:value={w.time} />
            </span>
          {/if}
        </div>
        {#if w.kind === 'custom'}
          <TextField
            class="mt-3 [&_input]:font-mono"
            label="Repeat rule"
            bind:value={w.rule}
            error={ruleError}
            help="Five fields: minute, hour, day, month, weekday. Example: 0 9 * * 1-5 means weekdays at 9:00."
            autocomplete="off"
            spellcheck="false"
            inputmode="text"
          />
        {/if}
        <p class="field-help" aria-live="polite">{previewLine}</p>
      </fieldset>

      <TextField class="mt-6" label="Name" bind:value={s.name} maxlength={80} oninput={() => (named = true)} />

      <!-- Each label sits above its control, as in the rest of the form. -->
      <Disclosure variant="section" label="More options" class="mt-6">
        <Select class="sm:max-w-64" label="Model" options={[{ value: 'main', label: 'Main model' }, { value: 'fast', label: 'Quick tasks model' }]} bind:value={s.slot} />
        <TextField class="mt-6 sm:max-w-32" label="Stop a run above" prefix="$" inputmode="decimal" placeholder="0.50" bind:value={cost} />
        <fieldset class="mt-6">
          <legend class="field-label">Only run between</legend>
          <div class="mt-2 flex items-center gap-2">
            <Select label="From" hideLabel options={[{ value: '', label: 'Any time' }, ...timeOptions(from)]} bind:value={from} />
            {#if from}<span class="text-body text-text">and</span><Select label="Until" hideLabel options={timeOptions(to)} bind:value={to} />{/if}
          </div>
        </fieldset>
        <Select class="mt-6 sm:max-w-64" label="If Bothy was off" options={[{ value: 'once', label: "Run when it's back" }, { value: 'skip', label: 'Skip that run' }]} bind:value={s.missed_run} />
        <Combobox class="mt-6 sm:max-w-64" label="Time zone" options={zones} bind:value={s.tz} />
        {#if saved.length || suggested.length}
          <fieldset class="mt-4">
            <legend class="field-label">Allowed without asking</legend>
            {#each [...saved, ...suggested] as g (g.id)}
              <Checkbox label={grantSentence(g)} checked={kept(g)} onchange={(e) => (keep[g.id] = e.currentTarget.checked)} />
            {/each}
          </fieldset>
        {/if}
      </Disclosure>

      <div class="mt-8 flex flex-wrap gap-2 max-sm:flex-col">
        <Button type="submit" variant="primary" loading={saving} loadingLabel="Saving…">{isNew ? 'Create schedule' : 'Save schedule'}</Button>
        <Button href="/activity/schedules">Cancel</Button>
      </div>
      {#if saveError}<p class="field-error" role="alert">{saveError}</p>{/if}
    </form>

    {#if !isNew}
      <section id="recent" class="mt-10">
        <h2 class="section-title">Recent runs</h2>
        {#if runs.length}
          <ul class="mt-2">
            <!-- The page names the schedule, so each run is named by when it ran and says how it went. -->
            {#each runs as r (r.id)}
              <TaskRow
                href="/activity/tasks/{encodeURIComponent(r.id)}"
                view={{ title: runTime(r.created_at), lead: [], outcome: taskView(r, { names, sessions: new Map(), schedules: new Map([[s.id, s]]), waiting: new Map() }).outcome }}
                cost={r.cost_micros ? money(r.cost_micros) : '—'}
                fullTime={fullTime(r.created_at)}
              />
            {/each}
          </ul>
        {:else}
          <p class="mt-2 text-body text-muted">No runs yet.</p>
        {/if}
      </section>
    {/if}
  </Page>

  <DeleteSchedule bind:schedule={removing} ondeleted={() => router.navigate('/activity/schedules')} />
{/if}
