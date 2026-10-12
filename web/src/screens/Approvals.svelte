<script lang="ts">
  import ApprovalCard from '../components/ApprovalCard.svelte';
  import Empty from '../components/Empty.svelte';
  import Icon from '../components/Icon.svelte';
  import { api, errorText, qs } from '../lib/api/client';
  import type { Approval, Choice, List } from '../lib/api/types.gen';
  import { ago } from '../lib/format';
  import { router } from '../lib/router.svelte';
  import { events } from '../lib/sse.svelte';
  import { clock } from '../lib/stores/clock.svelte';
  import { approvals } from '../lib/stores/session.svelte';

  let { params }: { params: Record<string, string> } = $props();

  type Tab = 'pending' | 'parked' | 'recent';
  const tabs: { v: Tab; label: string }[] = [
    { v: 'pending', label: 'Pending' },
    { v: 'parked', label: 'Parked' },
    { v: 'recent', label: 'Recent' },
  ];

  const tab = $derived((['pending', 'parked', 'recent'].includes(router.query.get('status') ?? '') ? router.query.get('status') : 'pending') as Tab);
  let recent: Approval[] = $state([]);
  let next = $state(0);
  let error = $state('');
  let focused: Approval | null = $state(null);
  let sel = $state(0);
  let listEl = $state<HTMLUListElement>();

  const list = $derived(
    tab === 'recent' ? recent : approvals.pending.filter((a) => a.status === tab).sort((x, y) => x.created_at.localeCompare(y.created_at)),
  );
  const counts = $derived({
    pending: approvals.pending.filter((a) => a.status === 'pending').length,
    parked: approvals.pending.filter((a) => a.status === 'parked').length,
    recent: 0,
  });

  async function loadRecent(more = false) {
    try {
      const r = await api.get<List<Approval>>(`/api/approvals${qs({ status: 'recent', limit: 30, before: more ? next : undefined })}`);
      recent = more ? [...recent, ...r.items] : r.items;
      next = r.next ?? 0;
    } catch (e) {
      error = errorText(e);
    }
  }

  $effect(() => {
    if (tab === 'recent') void loadRecent();
  });

  $effect(() => {
    const id = params.id;
    focused = null;
    if (id) api.get<Approval>(`/api/approvals/${encodeURIComponent(id)}`).then((a) => (focused = a), (e) => (error = errorText(e)));
  });

  $effect(() => {
    const offs = [
      events.on('approval.resolved', (e) => {
        if (tab === 'recent') void loadRecent();
        if (focused && (e.data as { approval_id: string }).approval_id === focused.id) void api.get<Approval>(`/api/approvals/${encodeURIComponent(focused.id)}`).then((a) => (focused = a));
      }),
      events.onResync(() => tab === 'recent' && void loadRecent()),
    ];
    return () => offs.forEach((f) => f());
  });

  $effect(() => {
    if (sel >= list.length) sel = Math.max(0, list.length - 1);
  });

  const keys: Record<string, Choice> = { a: 'allow_once', s: 'allow_session', l: 'allow_always', d: 'deny' };

  // Shortcuts go through the DOM buttons, so they obey exactly the same
  // checks (card choices, approver, busy) as a click.
  function onkeydown(e: KeyboardEvent) {
    if (e.metaKey || e.ctrlKey || e.altKey || e.defaultPrevented) return;
    if (e.target instanceof Element && e.target.closest('input, textarea, select, [contenteditable], dialog')) return;
    const row = () => listEl?.children[sel] as HTMLElement | undefined;
    if (e.key === 'j' || e.key === 'k') {
      e.preventDefault();
      sel = Math.min(Math.max(0, sel + (e.key === 'j' ? 1 : -1)), list.length - 1);
      row()?.scrollIntoView({ block: 'nearest' });
      row()?.querySelector<HTMLButtonElement>('footer button')?.focus();
    } else if (keys[e.key]) {
      const b = row()?.querySelector<HTMLButtonElement>(`button[data-choice="${keys[e.key]}"]`);
      if (!b) return;
      e.preventDefault();
      b.click();
    }
  }
</script>

<svelte:head><title>Approvals · Bothy</title></svelte:head>
<svelte:window {onkeydown} />

<div class="page max-w-3xl">
  <div class="mb-6 flex flex-wrap items-end gap-3">
    <div class="flex-1">
      <h1 class="page-title">Approvals</h1>
      <p class="mt-1 text-sm text-muted">Bothy asks here before anything risky. Deciding anywhere updates every chat app.</p>
    </div>
    <p class="hidden items-center gap-1.5 text-xs text-muted sm:flex" aria-hidden="true">
      <kbd class="kbd">j</kbd><kbd class="kbd">k</kbd> move · <kbd class="kbd">a</kbd> allow · <kbd class="kbd">s</kbd> session · <kbd class="kbd">d</kbd> deny
    </p>
  </div>

  {#if focused}
    <section class="mb-8" aria-label="Requested approval">
      <ApprovalCard approval={focused} onresolved={(a) => (focused = a)} />
      <p class="mt-2 flex gap-3 text-xs">
        <a class="link" href="/chat/{encodeURIComponent(focused.session_id)}" onclick={router.link}>Open the chat</a>
        <a class="link" href="/runs/{encodeURIComponent(focused.run_id)}" onclick={router.link}>Run trace</a>
      </p>
    </section>
  {/if}

  <div class="tabs mb-4" role="tablist" aria-label="Filter approvals">
    {#each tabs as t (t.v)}
      <button
        type="button"
        role="tab"
        class="tab"
        aria-selected={tab === t.v}
        onclick={() => router.setQuery({ status: t.v === 'pending' ? null : t.v })}
      >
        {t.label}{#if counts[t.v]}<span class="ml-1.5 rounded-full bg-warn-soft px-1.5 text-xs text-warn">{counts[t.v]}</span>{/if}
      </button>
    {/each}
  </div>

  {#if error}<p class="mb-4 alert-bad" role="alert">{error}</p>{/if}

  {#if !list.length}
    <Empty icon={tab === 'recent' ? 'clock' : 'check'} title={tab === 'pending' ? 'All clear' : tab === 'parked' ? 'Nothing parked' : 'No decisions yet'}>
      {tab === 'pending'
        ? 'Nothing is waiting for you. When Bothy needs a yes, it shows up here and in your chat apps.'
        : tab === 'parked'
          ? 'Scheduled runs park here when nobody answers in time, and resume once you decide.'
          : 'Approvals you and others decide will be listed here.'}
    </Empty>
  {:else}
    <ul class="space-y-4" bind:this={listEl}>
      {#each list as a, i (a.id)}
        <li class="rounded-xl transition-shadow {i === sel ? 'ring-2 ring-accent/50 ring-offset-4 ring-offset-bg' : ''}">
          <ApprovalCard approval={a} shortcuts={i === sel} />
          <p class="mt-1.5 flex gap-3 px-1 text-xs text-muted">
            <span>{ago(a.created_at, clock.now)}</span>
            <a class="link" href="/chat/{encodeURIComponent(a.session_id)}" onclick={router.link}>Open the chat</a>
            <a class="link" href="/runs/{encodeURIComponent(a.run_id)}" onclick={router.link}>Run trace</a>
          </p>
        </li>
      {/each}
    </ul>
    {#if tab === 'recent' && next}
      <button type="button" class="btn btn-ghost btn-sm mx-auto mt-4 flex" onclick={() => loadRecent(true)}><Icon name="down" size={14} />Older</button>
    {/if}
  {/if}
</div>
