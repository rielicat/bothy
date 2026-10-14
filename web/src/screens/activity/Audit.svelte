<script lang="ts">
  import { untrack } from 'svelte';
  import { Button, CodeBlock, Dialog, Disclosure, EmptyState, IconButton, LinkButton, Menu, Segmented, Select, StatusLine, toast } from '../../components/ui';
  import { api, qs } from '../../lib/api/client';
  import type { List } from '../../lib/api/types.gen';
  import { fullTime } from '../../lib/format';
  import { siteGrant } from '../../lib/grants';
  import { router } from '../../lib/router.svelte';
  import { session } from '../../lib/stores/session.svelte';
  import Activity from './Activity.svelte';
  import { people, type AuditCheck, type AuditRow, type EgressRow } from './api';
  import { actionGroups, actorName, resultWord, summarize, webLine, webOutcome } from './audit';
  import Loading from './Loading.svelte';
  import { groupByDay } from './tasks';

  const view = $derived(router.query.get('view') === 'web' ? 'web' : 'actions');
  const who = $derived(router.query.get('who') ?? '');
  const kind = $derived(router.query.get('action') ?? '');
  const period = $derived(router.query.get('period') ?? '7');

  let rows = $state<AuditRow[] | null>(null);
  let web = $state<EgressRow[] | null>(null);
  let next = $state(0);
  let names = $state(new Map<string, string>());
  let failed = $state(false);
  let expanded = $state<number | null>(null);
  let check = $state<AuditCheck | null>(null);
  let checking = $state(false);
  let allowing = $state<EgressRow | null>(null);
  let allowOpen = $state(false);
  let allowBusy = $state(false);
  let allowError = $state('');

  const since = (p: string) => (p === 'all' ? undefined : new Date(Date.now() - (p === 'today' ? 0 : Number(p) - 1) * 86_400_000).toISOString().slice(0, 10) + 'T00:00:00Z');

  async function load(more = false) {
    if (!session.isAdmin) return;
    failed = false;
    try {
      if (!more) names = await people();
      if (view === 'web') {
        const r = await api.get<List<EgressRow>>(`/api/egress${qs({ limit: 100, before: more ? next : undefined })}`);
        web = more ? [...(web ?? []), ...(r.items ?? [])] : (r.items ?? []);
        next = r.next ?? 0;
      } else {
        const r = await api.get<List<AuditRow>>(`/api/audit${qs({ actor: who.startsWith('user:') ? who : undefined, from: since(period), limit: 100, before: more ? next : undefined })}`);
        rows = more ? [...(rows ?? []), ...(r.items ?? [])] : (r.items ?? []);
        next = r.next ?? 0;
      }
    } catch {
      failed = true;
    }
  }

  $effect(() => {
    void [view, who, period];
    untrack(() => {
      rows = web = null;
      expanded = null;
      void load();
    });
  });

  const prefixes = $derived(actionGroups.find((g) => g.value === kind)?.prefixes ?? []);
  const shown = $derived(
    (rows ?? []).filter((e) => (who !== 'bothy' || /^(agent|system):?/.test(e.actor)) && (!prefixes.length || prefixes.some((p) => e.action.startsWith(p)))),
  );
  // "Last 7 days" is a filter too, and clearing goes to all time.
  const filtered = $derived(!!(who || kind || period !== 'all'));
  const whoOptions = $derived([
    { value: '', label: 'Anyone' },
    ...[...names].map(([id, n]) => ({ value: `user:${id}`, label: id === session.me?.user.id ? 'You' : n })),
    { value: 'bothy', label: 'Bothy' },
  ]);

  async function runCheck() {
    checking = true;
    try {
      check = await api.get<AuditCheck>('/api/audit/verify');
    } catch {
      check = { ok: false };
    } finally {
      checking = false;
    }
  }

  const badAt = $derived.by(() => {
    if (!check || check.ok) return '';
    const at = check.bad_at ?? rows?.find((r) => r.id === check?.bad_id)?.at;
    if (!at) return '';
    const d = new Date(at);
    return `${d.toLocaleDateString(undefined, { day: 'numeric', month: 'short' })} at ${d.toLocaleTimeString(undefined, { hour: 'numeric', minute: '2-digit' })}`;
  });

  function exportLog() {
    const blob = new Blob([JSON.stringify(view === 'web' ? web : rows, null, 2)], { type: 'application/json' });
    const a = document.createElement('a');
    a.href = URL.createObjectURL(blob);
    a.download = view === 'web' ? 'bothy-website-access.json' : 'bothy-audit-log.json';
    a.click();
    URL.revokeObjectURL(a.href);
  }

  async function allow() {
    if (!allowing) return;
    allowBusy = true;
    allowError = '';
    try {
      const principal = allowing.principal_id || session.principals[0]?.id || '';
      await api.post('/api/grants', siteGrant(principal, allowing.host, allowing.port));
      allowOpen = false;
      toast('Site allowed.');
    } catch {
      allowError = "Couldn't allow it. Try again.";
    } finally {
      allowBusy = false;
    }
  }

  const time = (t: string) => new Date(t).toLocaleTimeString(undefined, { hour: 'numeric', minute: '2-digit' });
  const clear = () => router.setQuery({ who: null, action: null, period: 'all' });
</script>

{#snippet headerActions()}
  <Button loading={checking} loadingLabel="Checking…" onclick={runCheck}>Check the log</Button>
  <Menu items={[{ label: 'Export', onselect: exportLog }]} placement="bottom-end">
    {#snippet trigger(props)}<IconButton icon="more" label="More for the audit log" {...props} />{/snippet}
  </Menu>
{/snippet}

<Activity title="Audit log" actions={session.isAdmin ? headerActions : undefined}>
  {#if !session.isAdmin}
    <EmptyState text="Only admins can see this page." action={{ label: 'Go to chat', href: '/chat' }} />
  {:else}
    {#if check}
      <StatusLine
        class="mb-6"
        alert
        tone={check.ok ? 'success' : 'danger'}
        text={check.ok
          ? `The log is intact.${check.checked ? ` ${check.checked.toLocaleString()} entries checked.` : ''}`
          : `${badAt ? `The entry from ${badAt}` : 'One entry'} doesn't match the ones before it. Entries from there on may have been changed.`}
        details={check.ok ? undefined : JSON.stringify(check)}
        action={check.ok ? undefined : { label: 'Export', onclick: exportLog }}
      />
    {/if}

    <Segmented
      label="View"
      options={[
        { value: 'actions', label: 'Actions' },
        { value: 'web', label: 'Website access' },
      ]}
      value={view}
      onchange={(v) => router.setQuery({ view: v === 'web' ? 'web' : null })}
    />

    {#if view === 'actions'}
      <div class="mt-4 flex flex-wrap gap-2">
        <Select label="Who" hideLabel options={whoOptions} value={who} onchange={(e) => router.setQuery({ who: e.currentTarget.value })} />
        <Select label="Action" hideLabel options={actionGroups} value={kind} onchange={(e) => router.setQuery({ action: e.currentTarget.value })} />
        <Select
          label="Period"
          hideLabel
          options={[
            { value: 'today', label: 'Today' },
            { value: '7', label: 'Last 7 days' },
            { value: '30', label: 'Last 30 days' },
            { value: 'all', label: 'All time' },
          ]}
          value={period}
          onchange={(e) => router.setQuery({ period: e.currentTarget.value === '7' ? null : e.currentTarget.value })}
        />
      </div>

      {#if rows === null}
        <div class="mt-6"><Loading {failed} retry={load} /></div>
      {:else if !shown.length}
        <EmptyState class="mt-6" text="Nothing matches these filters." action={filtered ? { label: 'Clear filters', variant: 'secondary', onclick: clear } : undefined} />
      {:else}
        {#each groupByDay(shown, (e) => e.at) as g (g.label)}
          <section class="mt-6">
            <h2 class="mb-1 text-caption text-muted">{g.label}</h2>
            <ul>
              {#each g.items as e (e.id)}
                {@const word = resultWord(e)}
                {@const actor = actorName(e.actor, names, e.actor_name)}
                <li class="border-b border-border last:border-b-0">
                  <button
                    type="button"
                    class="row-link flex min-h-10 w-[calc(100%+1rem)] cursor-pointer items-center gap-3 py-2 text-left text-body max-sm:flex-col max-sm:items-start max-sm:gap-0 pointer-coarse:min-h-12"
                    aria-expanded={expanded === e.id}
                    aria-controls="audit-{e.id}"
                    onclick={() => (expanded = expanded === e.id ? null : e.id)}
                  >
                    <time class="w-16 shrink-0 whitespace-nowrap text-muted tabular-nums max-sm:hidden" title={fullTime(e.at)}>{time(e.at)}</time>
                    <span class="w-30 shrink-0 truncate text-text max-sm:hidden">{actor}</span>
                    <span class="min-w-0 flex-1 text-text [overflow-wrap:anywhere]">{summarize(e)}</span>
                    <span class="text-small text-muted sm:hidden"
                      ><span class="whitespace-nowrap">{time(e.at)}</span> · {actor}{#if word}{' · '}<span class="text-danger-text">{word}</span>{/if}</span
                    >
                    {#if word}<span class="shrink-0 text-small text-danger-text max-sm:hidden">{word}</span>{/if}
                  </button>
                  <div id="audit-{e.id}" class="pb-3 text-small text-muted sm:pl-52" hidden={expanded !== e.id}>
                    {#if expanded === e.id}
                      <p>Who: <span class="text-text">{actor}</span></p>
                      <p>When: <span class="text-text">{fullTime(e.at)}</span></p>
                      {#if e.session_id}<p>Where: <a class="link" href="/chat/{encodeURIComponent(e.session_id)}" onclick={router.link}>Open the chat</a></p>{/if}
                      {#if e.run_id}<p>Task: <a class="link" href="/activity/tasks/{encodeURIComponent(e.run_id)}" onclick={router.link}>Open the task</a></p>{/if}
                      <Disclosure class="mt-1" label="Raw record"><CodeBlock code={JSON.stringify(e, null, 2)} wrap maxLines={16} /></Disclosure>
                    {/if}
                  </div>
                </li>
              {/each}
            </ul>
          </section>
        {/each}
      {/if}
    {:else if web === null}
      <div class="mt-6"><Loading {failed} retry={load} /></div>
    {:else if !web.length}
      <EmptyState class="mt-6" text="Bothy hasn't reached any websites yet." />
    {:else}
      {#each groupByDay(web, (e) => e.at) as g (g.label)}
        <section class="mt-6">
          <h2 class="mb-1 text-caption text-muted">{g.label}</h2>
          <ul>
            {#each g.items as e (e.id)}
              <!-- On phones the sentence, which names the site, leads and the time and task link fold under it. -->
              <li class="flex min-h-10 items-center gap-x-3 border-b border-border py-2 text-body last:border-b-0 max-sm:flex-col max-sm:items-start">
                <time class="w-16 shrink-0 whitespace-nowrap text-muted tabular-nums max-sm:hidden" title={fullTime(e.at)}>{time(e.at)}</time>
                <code class="w-48 shrink-0 truncate text-code text-text max-sm:hidden" title={e.host} data-copy-ignore>{e.host}</code>
                <span class="min-w-0 flex-1 [overflow-wrap:anywhere] {webOutcome(e) === 'blocked' ? 'text-danger-text' : 'text-text'}">
                  {webLine(e)}{#if webOutcome(e) === 'blocked'}{' '}<LinkButton onclick={() => ((allowing = e), (allowOpen = true))}>Allow this site…</LinkButton>{/if}
                </span>
                <span class="flex items-center gap-1.5 text-small text-muted">
                  <time class="whitespace-nowrap tabular-nums sm:hidden" title={fullTime(e.at)}>{time(e.at)}</time>
                  {#if e.run_id}<span class="sm:hidden" aria-hidden="true">·</span
                    ><a class="link inline-flex min-h-6 items-center pointer-coarse:min-h-11 pointer-coarse:min-w-11" href="/activity/tasks/{encodeURIComponent(e.run_id)}" onclick={router.link}>Task</a>{/if}
                </span>
              </li>
            {/each}
          </ul>
        </section>
      {/each}
    {/if}

    {#if next && ((view === 'web' && web?.length) || (view === 'actions' && rows?.length))}
      <Button class="mt-6" size="sm" onclick={() => load(true)}>Show earlier</Button>
    {/if}
  {/if}
</Activity>

<Dialog bind:open={allowOpen} title="Allow Bothy to send data to {allowing?.host ?? ''}?" busy={allowBusy} onclose={() => (allowError = '')}>
  <p class="text-body text-text">This applies to everyone's tasks until you remove it in Settings → Security.</p>
  {#if allowError}<p class="field-error mt-2" role="alert">{allowError}</p>{/if}
  {#snippet footer({ close })}
    <Button data-cancel onclick={close}>Cancel</Button>
    <Button variant="primary" loading={allowBusy} loadingLabel="Allowing…" onclick={allow}>Allow site</Button>
  {/snippet}
</Dialog>
