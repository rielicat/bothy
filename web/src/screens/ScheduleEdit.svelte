<script lang="ts">
  import Dialog from '../components/Dialog.svelte';
  import Icon from '../components/Icon.svelte';
  import { api, errorText } from '../lib/api/client';
  import type { ChannelIdentity, ChatRef, Grant, GrantKind, List, Run, Schedule, ScheduleKind, SchedulePreviewResp, SideEffect } from '../lib/api/types.gen';
  import { usd, when } from '../lib/format';
  import { risk, statusOf } from '../lib/labels';
  import { router } from '../lib/router.svelte';
  import { cronPresets, localZone, saveSchedule, scheduleGrants, timezones, ZERO } from '../lib/schedules';
  import { session } from '../lib/stores/session.svelte';
  import { toasts } from '../lib/stores/ui.svelte';

  let { params }: { params: Record<string, string> } = $props();

  const isNew = $derived(params.id === 'new');
  let s = $state<Schedule | null>(null);
  let grants = $state<(Grant & { keep: boolean })[]>([]);
  let runs = $state<Run[]>([]);
  let identities = $state<ChannelIdentity[]>([]);
  let preview = $state<SchedulePreviewResp | null>(null);
  let previewError = $state('');
  let error = $state('');
  let saving = $state(false);
  let confirmDelete = $state(false);
  let ahFrom = $state('');
  let ahTo = $state('');
  let g = $state({ kind: 'domain' as GrantKind, tool: '', pointer: '', glob: '', value: '', max_se: 'external_send' as SideEffect });

  const zones = timezones();

  function blank(): Schedule {
    return {
      id: '',
      principal_id: session.me?.user.id ?? '',
      name: '',
      kind: 'cron',
      spec: '0 8 * * *',
      tz: session.me?.user.timezone || localZone(),
      active_hours: '',
      prompt: '',
      slot: 'main',
      limits: { max_steps: 0, max_tokens: 0, max_time: 0, max_cost_micros: 0 },
      missed_run: 'skip',
      enabled: true,
      created_by: '',
      failures: 0,
      next_run_at: '',
      last_run_at: '',
      created_at: '',
      updated_at: '',
    };
  }

  $effect(() => {
    const id = params.id;
    error = '';
    api.get<ChannelIdentity[]>('/api/identities').then((l) => (identities = l.filter((i) => i.status === 'linked' && i.dm_chat_id)), () => {});
    if (id === 'new') {
      s = blank();
      grants = [];
      runs = [];
      return;
    }
    Promise.all([
      api.get<Schedule>(`/api/schedules/${encodeURIComponent(id)}`),
      scheduleGrants(id),
      api.get<List<Run>>(`/api/schedules/${encodeURIComponent(id)}/runs?limit=20`),
    ])
      .then(([sc, gr, rn]) => {
        s = sc;
        [ahFrom, ahTo] = (sc.active_hours ?? '').split('-');
        grants = gr.map((x) => ({ ...x, keep: true }));
        runs = rn.items ?? [];
      })
      .catch((e) => (error = errorText(e)));
  });

  let timer: ReturnType<typeof setTimeout> | undefined;
  $effect(() => {
    if (!s) return;
    const req = { kind: s.kind, spec: s.spec, tz: s.tz };
    clearTimeout(timer);
    timer = setTimeout(async () => {
      try {
        preview = await api.post<SchedulePreviewResp>('/api/schedules/preview', req);
        previewError = '';
      } catch (e) {
        preview = null;
        previewError = errorText(e);
      }
    }, 300);
  });

  const deliverValue = $derived(!s?.deliver ? 'preferred' : s.deliver.channel === 'web' ? 'web' : `${s.deliver.channel}:${s.deliver.chat_id}`);

  function setDeliver(v: string) {
    if (!s) return;
    if (v === 'preferred') s.deliver = undefined;
    else if (v === 'web') s.deliver = { channel: 'web', chat_id: '', kind: 'web' };
    else {
      const i = identities.find((x) => `${x.channel}:${x.dm_chat_id}` === v);
      s.deliver = i ? ({ channel: i.channel, chat_id: i.dm_chat_id!, kind: 'dm' } satisfies ChatRef) : undefined;
    }
  }

  function setKind(k: ScheduleKind) {
    if (!s || s.kind === k) return;
    s.kind = k;
    s.spec = k === 'cron' ? '0 8 * * *' : k === 'heartbeat' ? '30m' : new Date(Date.now() + 3600_000).toISOString().slice(0, 16);
  }

  function addGrant() {
    if (!s) return;
    const tool = g.kind === 'tool';
    if (tool && (!g.tool || !g.pointer || !g.glob)) return toasts.push('A tool grant needs a tool and one argument pattern, so it stays narrow.', 'error');
    if (!tool && !g.value) return;
    grants.push({
      id: '',
      principal_id: s.principal_id,
      scope: 'schedule',
      scope_id: s.id,
      kind: g.kind,
      tool: tool ? g.tool : undefined,
      pattern: tool ? { [g.pointer]: g.glob } : undefined,
      value: tool ? undefined : g.value,
      max_se: g.max_se,
      allow_when_tainted: false,
      uses: 0,
      max_uses: 0,
      created_by: '',
      created_at: ZERO,
      expires_at: ZERO,
      keep: true,
    });
    g = { ...g, tool: '', pointer: '', glob: '', value: '' };
  }

  async function save(e: SubmitEvent) {
    e.preventDefault();
    if (!s) return;
    saving = true;
    s.active_hours = ahFrom && ahTo ? `${ahFrom}-${ahTo}` : '';
    try {
      const keep = grants.filter((x) => x.keep).map(({ keep: _, ...x }) => x);
      const out = await saveSchedule($state.snapshot(s), keep);
      toasts.ok(isNew ? 'Schedule created.' : 'Schedule saved.');
      if (isNew) router.navigate(`/schedules/${encodeURIComponent(out.id)}`, { replace: true });
      else s = out;
    } catch (err) {
      toasts.error(err);
    } finally {
      saving = false;
    }
  }

  async function remove() {
    try {
      await api.del(`/api/schedules/${encodeURIComponent(params.id)}`);
      router.navigate('/schedules');
    } catch (e) {
      toasts.error(e);
    }
  }

  const grantText = (x: Grant) => (x.kind === 'tool' ? `${x.tool} where ${Object.entries(x.pattern ?? {}).map(([k, v]) => `${k} matches ${v}`).join(', ')}` : `${x.kind === 'domain' ? 'Connect to' : 'Send to'} ${x.value}`);
  const toMin = (ns: number) => (ns ? Math.round(ns / 60e9) : '');
</script>

<svelte:head><title>{isNew ? 'New schedule' : s?.name || 'Schedule'} · Bothy</title></svelte:head>

<div class="page max-w-3xl">
  <a href="/schedules" onclick={router.link} class="mb-4 inline-flex items-center gap-1 text-sm text-muted hover:text-fg"><Icon name="left" size={15} />Schedules</a>
  {#if error}
    <p class="alert-bad" role="alert">{error}</p>
  {:else if s}
    <h1 class="page-title mb-6">{isNew ? 'New schedule' : s.name}</h1>
    <form class="space-y-6" onsubmit={save}>
      <section class="card space-y-4 p-5">
        <div>
          <label class="label" for="sc-name">Name</label>
          <input id="sc-name" class="input" required maxlength="80" placeholder="Morning briefing" bind:value={s.name} />
        </div>
        <fieldset>
          <legend class="label">When</legend>
          <div class="grid grid-cols-3 gap-2">
            {#each [['cron', 'Repeating'], ['heartbeat', 'Heartbeat'], ['once', 'Once']] as [k, l] (k)}
              <button type="button" class="btn {s.kind === k ? 'border-accent bg-accent-soft text-accent' : ''}" aria-pressed={s.kind === k} onclick={() => setKind(k as ScheduleKind)}>{l}</button>
            {/each}
          </div>
        </fieldset>
        {#if s.kind === 'cron'}
          <div class="flex flex-wrap gap-1.5">
            {#each cronPresets as p (p.spec)}
              <button type="button" class="chip hover:border-accent {s.spec === p.spec ? 'chip-accent' : ''}" onclick={() => (s!.spec = p.spec)}>{p.label}</button>
            {/each}
          </div>
          <div>
            <label class="label" for="sc-spec">Cron expression</label>
            <input id="sc-spec" class="input mono" bind:value={s.spec} aria-describedby="sc-prev" />
          </div>
        {:else if s.kind === 'heartbeat'}
          <div>
            <label class="label" for="sc-spec">Every</label>
            <input id="sc-spec" class="input mono" placeholder="30m" bind:value={s.spec} aria-describedby="sc-prev" />
            <p class="help">Bothy reads your HEARTBEAT.md each time and only messages you when something needs attention.</p>
          </div>
        {:else}
          <div>
            <label class="label" for="sc-spec">Date and time</label>
            <input id="sc-spec" class="input" type="datetime-local" bind:value={s.spec} aria-describedby="sc-prev" />
          </div>
        {/if}
        <div id="sc-prev" class="rounded-lg bg-sunken px-3 py-2 text-sm" aria-live="polite">
          {#if previewError}<span class="text-bad">{previewError}</span>
          {:else if preview}
            <p class="font-medium">{preview.human}</p>
            <p class="mt-1 text-xs text-muted">Next: {preview.next.slice(0, 5).map((t) => when(t)).join(' · ')}</p>
          {:else}<span class="text-muted">Checking…</span>{/if}
        </div>
        <div class="grid gap-4 sm:grid-cols-2">
          <div>
            <label class="label" for="sc-tz">Timezone</label>
            <input id="sc-tz" class="input" list="sc-zones" bind:value={s.tz} />
            <datalist id="sc-zones">{#each zones as z (z)}<option value={z}></option>{/each}</datalist>
          </div>
          <div>
            <span class="label" id="sc-ah">Active hours <span class="font-normal text-muted">(optional)</span></span>
            <div class="flex items-center gap-2" role="group" aria-labelledby="sc-ah">
              <input class="input" type="time" aria-label="From" bind:value={ahFrom} /><span class="text-muted">–</span><input class="input" type="time" aria-label="To" bind:value={ahTo} />
            </div>
          </div>
        </div>
      </section>

      <section class="card space-y-4 p-5">
        <div>
          <label class="label" for="sc-prompt">What should Bothy do?</label>
          <textarea id="sc-prompt" class="input min-h-28" required placeholder="Check the weather and my calendar, then send me a short plan for the day." bind:value={s.prompt}></textarea>
        </div>
        <div class="grid gap-4 sm:grid-cols-2">
          <div>
            <label class="label" for="sc-deliver">Send the result to</label>
            <select id="sc-deliver" class="input" value={deliverValue} onchange={(e) => setDeliver(e.currentTarget.value)}>
              <option value="preferred">My preferred chat app</option>
              <option value="web">Web only</option>
              {#each identities as i (i.channel + i.external_id)}<option value="{i.channel}:{i.dm_chat_id}">{i.channel}: {i.display_name || i.username}</option>{/each}
            </select>
          </div>
          <div>
            <label class="label" for="sc-slot">Model</label>
            <select id="sc-slot" class="input" bind:value={s.slot}>
              <option value="main">Main (smarter)</option>
              <option value="fast">Fast (cheaper)</option>
            </select>
          </div>
        </div>
        <details class="rounded-lg border border-line px-3 py-2">
          <summary class="cursor-pointer text-sm font-medium">Limits and missed runs</summary>
          <div class="mt-3 grid gap-3 sm:grid-cols-3">
            <div><label class="label" for="sc-steps">Max steps</label><input id="sc-steps" class="input" type="number" min="0" placeholder="default" bind:value={s.limits.max_steps} /></div>
            <div>
              <label class="label" for="sc-time">Max minutes</label>
              <input id="sc-time" class="input" type="number" min="0" placeholder="default" value={toMin(s.limits.max_time)} oninput={(e) => (s!.limits.max_time = Number(e.currentTarget.value) * 60e9)} />
            </div>
            <div>
              <label class="label" for="sc-cost">Max $ per run</label>
              <input id="sc-cost" class="input" type="number" min="0" step="0.01" placeholder="default" value={s.limits.max_cost_micros ? s.limits.max_cost_micros / 1e6 : ''} oninput={(e) => (s!.limits.max_cost_micros = Math.round(Number(e.currentTarget.value) * 1e6))} />
            </div>
          </div>
          <div class="mt-3">
            <label class="label" for="sc-missed">If Bothy was down at run time</label>
            <select id="sc-missed" class="input" bind:value={s.missed_run}>
              <option value="skip">Skip it</option>
              <option value="once">Run once when back (within an hour)</option>
            </select>
          </div>
        </details>
      </section>

      <section class="card space-y-3 p-5">
        <div>
          <h2 class="section-title">Pre-approved actions</h2>
          <p class="help">Scheduled runs can't wait for you, so risky actions park. Pre-approve narrow ones here; admin and financial actions can never be pre-approved.</p>
        </div>
        {#if grants.length}
          <ul class="space-y-2">
            {#each grants as x, i (x.id || i)}
              <li class="flex items-center gap-3 rounded-lg border border-line px-3 py-2 text-sm">
                <input type="checkbox" class="size-4 accent-accent" bind:checked={x.keep} aria-label="Keep {grantText(x)}" />
                <span class="min-w-0 flex-1 truncate">{grantText(x)}</span>
                <span class="rounded-full border px-2 py-0.5 text-xs {risk[x.max_se]?.cls ?? ''}">{risk[x.max_se]?.label ?? x.max_se}</span>
              </li>
            {/each}
          </ul>
        {/if}
        <!-- svelte-ignore a11y_no_static_element_interactions -->
        <div class="grid gap-2 sm:grid-cols-[auto_1fr_auto_auto]" onkeydown={(e) => e.key === 'Enter' && (e.preventDefault(), addGrant())}>
          <select class="input" aria-label="Grant kind" bind:value={g.kind}>
            <option value="domain">Website</option>
            <option value="recipient">Recipient</option>
            <option value="tool">Tool call</option>
          </select>
          {#if g.kind === 'tool'}
            <div class="grid grid-cols-3 gap-2">
              <input class="input" placeholder="shell" aria-label="Tool" bind:value={g.tool} />
              <input class="input mono" placeholder="/argv/0" aria-label="Argument pointer" bind:value={g.pointer} />
              <input class="input mono" placeholder="ls" aria-label="Allowed pattern" bind:value={g.glob} />
            </div>
          {:else}
            <input class="input" placeholder={g.kind === 'domain' ? 'api.example.com' : 'someone@example.com'} aria-label="Value" bind:value={g.value} />
          {/if}
          <select class="input" aria-label="Highest class allowed" bind:value={g.max_se}>
            {#each ['read', 'local_write', 'external_send', 'irreversible'] as c (c)}<option value={c}>{risk[c as SideEffect].label}</option>{/each}
          </select>
          <button type="button" class="btn" onclick={addGrant}>Add</button>
        </div>
      </section>

      <div class="flex flex-wrap items-center gap-3">
        <label class="flex items-center gap-2 text-sm"><input type="checkbox" class="switch" bind:checked={s.enabled} />Enabled</label>
        <div class="flex-1"></div>
        {#if !isNew}<button type="button" class="btn btn-ghost text-bad" onclick={() => (confirmDelete = true)}><Icon name="trash" size={15} />Delete</button>{/if}
        <button class="btn btn-primary" disabled={saving}>{saving ? 'Saving…' : isNew ? 'Create schedule' : 'Save'}</button>
      </div>
    </form>

    {#if runs.length}
      <h2 class="mt-10 mb-3 section-title">History</h2>
      <div class="card overflow-x-auto">
        <table class="table">
          <thead><tr><th>Started</th><th>Status</th><th class="text-right">Cost</th><th></th></tr></thead>
          <tbody>
            {#each runs as r (r.id)}
              <tr>
                <td class="whitespace-nowrap">{when(r.started_at || r.created_at)}</td>
                <td><span class={statusOf(r.status).cls}>{statusOf(r.status).label}</span></td>
                <td class="text-right tabular-nums">{usd(r.cost_micros)}</td>
                <td class="text-right whitespace-nowrap">
                  <a class="link" href="/chat/{encodeURIComponent(r.session_id)}" onclick={router.link}>Transcript</a> ·
                  <a class="link" href="/runs/{encodeURIComponent(r.id)}" onclick={router.link}>Trace</a>
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  {:else}
    <div class="skeleton h-96"></div>
  {/if}
</div>

<Dialog bind:open={confirmDelete} title="Delete this schedule?">
  <p class="text-sm text-muted">Its pre-approved actions are revoked too. Past runs stay in the history.</p>
  {#snippet footer()}
    <button type="button" class="btn" onclick={() => (confirmDelete = false)}>Cancel</button>
    <button type="button" class="btn btn-danger" onclick={remove}>Delete</button>
  {/snippet}
</Dialog>
