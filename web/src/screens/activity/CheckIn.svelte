<script lang="ts">
  import { untrack } from 'svelte';
  import { Button, LinkButton, Page, Select, SettingsRow, TextArea, toast } from '../../components/ui';
  import { api, ApiError } from '../../lib/api/client';
  import type { ConfigView, List, MemoryDoc, ModelInfo, Schedule, ScheduleExamples } from '../../lib/api/types.gen';
  import { estimate } from '../../lib/format';
  import { router } from '../../lib/router.svelte';
  import { saveSchedule, scheduleGrants, timeOptions } from '../../lib/schedules';
  import { session } from '../../lib/stores/session.svelte';
  import type { TaskRun } from './api';
  import Loading from '../../components/ui/Loading.svelte';
  import { checkinBlank, checkinFile, checkinText } from './schedule';

  let s = $state<Schedule | null>(null);
  let doc = $state<MemoryDoc | null>(null);
  let list = $state('');
  // The scheduler reads the file as it is, so checkbox markers mean nothing to it; the form shows plain lines.
  let items = $state(true);
  let from = $state('08:00');
  let to = $state('22:00');
  let model = $state<ModelInfo | null>(null);
  let perCheck = $state(0);
  let saving = $state(false);
  let error = $state('');
  let conflict = $state(false);
  let failed = $state(false);
  let lines = $state<string[]>([]);
  let box = $state<HTMLTextAreaElement>();
  let tried = $state(false);
  // The scheduler skips an empty list, so a check-in saved on with one would never do anything.
  const listError = $derived(tried && s?.enabled && !list.trim() ? 'Add something to check first.' : '');

  async function load() {
    failed = false;
    try {
      const [all, d, models, config, ex] = await Promise.all([
        api.get<Schedule[]>('/api/schedules'),
        api.get<MemoryDoc>('/api/memory/HEARTBEAT'),
        api.get<ModelInfo[]>('/api/models').catch(() => [] as ModelInfo[]),
        session.isAdmin ? api.get<ConfigView>('/api/config').catch(() => null) : null,
        api.get<ScheduleExamples>('/api/schedules/examples').catch(() => null),
      ]);
      const found = all.find((x) => x.kind === 'heartbeat');
      s = found ?? { ...checkinBlank(session.me?.user), enabled: true };
      // Turning the check-in on with an empty list opens this form, which saves it on.
      if (router.query.has('on')) s.enabled = true;
      lines = ex?.checkin.lines ?? [];
      doc = d;
      ({ text: list, items } = checkinText(d.content));
      [from, to] = (s.active_hours || '08:00-22:00').split('-');
      // S5 will name the Quick tasks model in /api/models; until then admins read it from the config.
      const fast = (config?.entries.find((e) => e.path === 'models.fast')?.value as string[] | undefined)?.[0];
      model = models.find((m) => `${m.ref.provider}/${m.ref.model}` === fast) ?? null;
      const recent = found ? await api.get<List<TaskRun>>(`/api/schedules/${encodeURIComponent(found.id)}/runs?limit=10`).catch(() => null) : null;
      const costs = (recent?.items ?? []).map((r) => r.cost_micros).filter((c) => c > 0);
      perCheck = costs.length ? costs.reduce((a, b) => a + b, 0) / costs.length : model ? (3000 * model.price.in_per_m + 200 * model.price.out_per_m) / 1e6 : 0;
    } catch {
      failed = true;
    }
  }

  $effect(() => {
    untrack(() => void load());
  });

  const minutes = (hm: string) => Number(hm.slice(0, 2)) * 60 + Number(hm.slice(3, 5));
  const interval = $derived(s?.spec === '15m' ? 15 : s?.spec === '1h' || s?.spec === '60m' ? 60 : 30);
  const costLine = $derived.by(() => {
    const name = model?.display ?? 'the Quick tasks model';
    if (model?.local) return `Free with ${name}.`;
    if (!perCheck || (model && !model.price.known)) return '';
    const span = (minutes(to) - minutes(from) + 1440) % 1440 || 1440;
    const day = estimate(perCheck * Math.ceil(span / interval));
    return `${day[0].toUpperCase()}${day.slice(1)} a day with ${name}.`;
  });

  async function save(e: SubmitEvent) {
    e.preventDefault();
    if (!s || !doc) return;
    tried = true;
    if (listError) return box?.focus();
    saving = true;
    error = '';
    conflict = false;
    try {
      const content = checkinFile(list, items);
      if (content !== doc.content) doc = await api.put<MemoryDoc>('/api/memory/HEARTBEAT', { content }, { 'If-Match': doc.etag });
      await saveSchedule({ ...$state.snapshot(s), active_hours: `${from}-${to}` }, s.id ? await scheduleGrants(s.id) : []);
      toast('Saved.');
      router.navigate('/activity/schedules');
    } catch (err) {
      conflict = err instanceof ApiError && err.status === 409;
      error = conflict ? 'Someone else changed this list while you were editing.' : "Couldn't save. Try again.";
    } finally {
      saving = false;
    }
  }
</script>

<Page title="Check-in" back={{ href: '/activity/schedules', label: 'Schedules' }}>
  {#if !s || !doc}
    <Loading {failed} retry={load} />
  {:else}
    <form onsubmit={save}>
      <div class="list-card">
        <SettingsRow label="How often">
          {#snippet children(c)}
            <Select
              id={c.id}
              options={[
                { value: '15m', label: 'Every 15 min' },
                { value: '30m', label: 'Every 30 min' },
                { value: '1h', label: 'Every hour' },
              ]}
              value={interval === 15 ? '15m' : interval === 60 ? '1h' : '30m'}
              onchange={(e) => s && (s.spec = e.currentTarget.value)}
            />
          {/snippet}
        </SettingsRow>
        <SettingsRow label="Between">
          {#snippet children(c)}
            <div role="group" aria-labelledby={c.labelId} class="flex items-center gap-2">
              <Select class="min-w-0 flex-1" label="From" hideLabel options={timeOptions(from)} bind:value={from} />
              <span class="text-body text-text">and</span>
              <Select class="min-w-0 flex-1" label="Until" hideLabel options={timeOptions(to)} bind:value={to} />
            </div>
          {/snippet}
        </SettingsRow>
        <div class="border-b border-border py-4">
          <TextArea
            label="What to check"
            bind:value={list}
            rows={5}
            maxRows={16}
            placeholder="One thing per line, for example: Tell me if a deploy to production failed overnight"
            help="Bothy looks through this list regularly and messages you only when something needs attention."
            error={listError}
            bind:textarea={box}
          />
          {#if lines.length && !list.trim()}
            <LinkButton
              class="mt-2"
              onclick={() => {
                list = lines.join('\n');
                box?.focus();
              }}>Use suggested lines</LinkButton
            >
          {/if}
        </div>
      </div>
      {#if costLine}<p class="mt-3 text-small text-muted">{costLine}</p>{/if}
      <div class="mt-8 flex flex-wrap gap-2 max-sm:flex-col">
        <Button type="submit" variant="primary" loading={saving} loadingLabel="Saving…">Save</Button>
        <Button href="/activity/schedules">Cancel</Button>
      </div>
      {#if error}
        <p class="field-error" role="alert">
          <span>{error}{#if conflict}{' '}<LinkButton onclick={load}>Reload</LinkButton>{/if}</span>
        </p>
      {/if}
    </form>
  {/if}
</Page>
