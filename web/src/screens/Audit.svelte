<script lang="ts">
  import { untrack } from 'svelte';
  import Empty from '../components/Empty.svelte';
  import Icon from '../components/Icon.svelte';
  import Verbatim from '../components/Verbatim.svelte';
  import { api, errorText, qs } from '../lib/api/client';
  import type { AuditEntry, AuditVerify, EgressRecord, List } from '../lib/api/types.gen';
  import { bytes, when } from '../lib/format';
  import { appendNew } from '../lib/list';
  import { router } from '../lib/router.svelte';
  import { session } from '../lib/stores/session.svelte';

  const tab = $derived(router.query.get('tab') === 'egress' ? 'egress' : 'log');
  let actor = $state('');
  let action = $state('');
  let from = $state('');
  let to = $state('');

  let rows = $state<AuditEntry[]>([]);
  let egress = $state<EgressRecord[]>([]);
  let next = $state(0);
  let open = $state<number | null>(null);
  let error = $state('');
  let verify = $state<AuditVerify | null>(null);
  let verifying = $state(false);

  const prefixes = ['auth.', 'token.', 'config.', 'vault.', 'secret.', 'tool.', 'approval.', 'grant.', 'egress.', 'memory.', 'skill.', 'mcp.', 'schedule.', 'admin.'];

  function params(before?: number) {
    return qs({
      actor,
      action,
      from: from ? new Date(`${from}T00:00:00`).toISOString() : undefined,
      to: to ? new Date(`${to}T23:59:59`).toISOString() : undefined,
      before,
      limit: 50,
    });
  }

  async function load(more = false) {
    error = '';
    try {
      if (tab === 'log') {
        const r = await api.get<List<AuditEntry>>(`/api/audit${params(more ? next : undefined)}`);
        rows = more ? appendNew(rows, r.items, (x) => x.id) : (r.items ?? []);
        next = r.next ?? 0;
      } else {
        const r = await api.get<List<EgressRecord>>(`/api/egress${qs({ limit: 100, before: more ? next : undefined })}`);
        egress = more ? appendNew(egress, r.items, (x) => x.id) : (r.items ?? []);
        next = r.next ?? 0;
      }
    } catch (e) {
      error = errorText(e);
    }
  }

  // The URL is the applied filter: typing in the boxes changes nothing until
  // Filter, and back/forward restore the boxes along with the results.
  $effect(() => {
    void tab;
    const q = router.query;
    actor = q.get('actor') ?? '';
    action = q.get('action') ?? '';
    from = q.get('from') ?? '';
    to = q.get('to') ?? '';
    untrack(() => void load());
  });

  function apply(e: SubmitEvent) {
    e.preventDefault();
    const before = router.search;
    router.setQuery({ actor, action, from, to });
    if (router.search === before) void load();
  }

  async function runVerify() {
    verifying = true;
    try {
      verify = await api.get<AuditVerify>('/api/audit/verify');
    } catch (e) {
      error = errorText(e);
    } finally {
      verifying = false;
    }
  }
</script>

<svelte:head><title>Audit · Bothy</title></svelte:head>

<div class="page">
  {#if !session.isAdmin}
    <Empty icon="lock" title="Admins only">The audit log is visible to owners and admins.</Empty>
  {:else}
    <div class="mb-6 flex flex-wrap items-end gap-3">
      <div class="flex-1">
        <h1 class="page-title">Audit log</h1>
        <p class="mt-1 text-sm text-muted">Append-only and hash-chained. Details are redacted and never include message content.</p>
      </div>
      <button type="button" class="btn" onclick={runVerify} disabled={verifying}><Icon name="shield" size={16} />{verifying ? 'Verifying…' : 'Verify chain'}</button>
    </div>

    {#if verify}
      <p class="{verify.ok ? 'alert-ok' : 'alert-bad'} mb-4" role="status">
        <Icon name={verify.ok ? 'check' : 'alert'} size={16} />
        {verify.ok ? 'The chain is intact: no entry was changed or removed.' : `The chain breaks at entry ${verify.bad_id}. Entries from there on may have been tampered with.`}
      </p>
    {/if}

    <div class="tabs mb-4" role="tablist" aria-label="Audit views">
      <button type="button" role="tab" class="tab" aria-selected={tab === 'log'} onclick={() => router.navigate('/audit')}>Events</button>
      <button type="button" role="tab" class="tab" aria-selected={tab === 'egress'} onclick={() => router.navigate('/audit?tab=egress')}>Network</button>
    </div>

    {#if error}<p class="alert-bad mb-4" role="alert">{error}</p>{/if}

    {#if tab === 'log'}
      <form class="mb-4 grid gap-2 sm:grid-cols-[1fr_1fr_auto_auto_auto]" onsubmit={apply}>
        <input class="input" placeholder="Actor, e.g. user:usr_…" aria-label="Actor" bind:value={actor} />
        <input class="input" placeholder="Action prefix, e.g. vault." aria-label="Action prefix" list="audit-prefixes" bind:value={action} />
        <datalist id="audit-prefixes">{#each prefixes as p (p)}<option value={p}></option>{/each}</datalist>
        <input class="input" type="date" aria-label="From" bind:value={from} />
        <input class="input" type="date" aria-label="To" bind:value={to} />
        <button class="btn">Filter</button>
      </form>
      {#if !rows.length}
        <Empty icon="shield" title="No matching entries">Try a wider time range or a shorter action prefix.</Empty>
      {:else}
        <div class="card overflow-x-auto">
          <table class="table">
            <thead><tr><th>When</th><th>Actor</th><th>Action</th><th>Target</th><th>Outcome</th></tr></thead>
            <tbody>
              {#each rows as r (r.id)}
                <tr class="hover:bg-sunken/60">
                  <td class="whitespace-nowrap text-muted">{when(r.at)}</td>
                  <td class="mono max-w-40 truncate">{r.actor}</td>
                  <td>
                    <button type="button" class="mono text-left underline decoration-line-strong underline-offset-2" aria-expanded={open === r.id} onclick={() => (open = open === r.id ? null : r.id)}>{r.action}</button>
                  </td>
                  <td class="mono max-w-48 truncate">{r.target}</td>
                  <td><span class="chip {r.outcome === 'ok' ? 'chip-ok' : r.outcome === 'denied' ? 'chip-warn' : 'chip-bad'}">{r.outcome}</span></td>
                </tr>
                {#if open === r.id}
                  <tr>
                    <td colspan="5" class="bg-sunken/50">
                      <p class="mb-2 flex flex-wrap gap-3 text-xs text-muted">
                        <span>#{r.id}</span>
                        {#if r.ip}<span>IP {r.ip}</span>{/if}
                        {#if r.session_id}<a class="link" href="/chat/{encodeURIComponent(r.session_id)}" onclick={router.link}>chat</a>{/if}
                        {#if r.run_id}<a class="link" href="/runs/{encodeURIComponent(r.run_id)}" onclick={router.link}>run</a>{/if}
                      </p>
                      <pre class="mono max-h-80 overflow-auto rounded-lg bg-surface p-3 whitespace-pre-wrap break-all"><Verbatim text={JSON.stringify(r.detail, null, 2) ?? ''} /></pre>
                    </td>
                  </tr>
                {/if}
              {/each}
            </tbody>
          </table>
        </div>
      {/if}
    {:else if !egress.length}
      <Empty icon="globe" title="No network activity yet">Every connection a sandbox box or Bothy itself makes is logged here.</Empty>
    {:else}
      <div class="card overflow-x-auto">
        <table class="table">
          <thead><tr><th>When</th><th>Destination</th><th>From</th><th>Decision</th><th class="text-right">Up / down</th></tr></thead>
          <tbody>
            {#each egress as e (e.id)}
              <tr>
                <td class="whitespace-nowrap text-muted">{when(e.at)}</td>
                <td class="mono">{e.host}:{e.port}</td>
                <td>{e.dialer}{e.dest ? ` · ${e.dest}` : ''}</td>
                <td>
                  <span class="chip {e.decision.includes('allow') ? 'chip-ok' : 'chip-bad'}">{e.decision}</span>
                  {#if e.reason}<span class="block text-xs text-muted">{e.reason}</span>{/if}
                </td>
                <td class="text-right whitespace-nowrap tabular-nums">{bytes(e.bytes_up)} / {bytes(e.bytes_down)}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
    {#if next}
      <button type="button" class="btn btn-ghost btn-sm mx-auto mt-4 flex" onclick={() => load(true)}><Icon name="down" size={14} />Older</button>
    {/if}
  {/if}
</div>
