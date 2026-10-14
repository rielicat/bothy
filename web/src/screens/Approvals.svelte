<script lang="ts">
  import { tick, untrack } from 'svelte';
  import { cubicOut } from 'svelte/easing';
  import { slide } from 'svelte/transition';
  import { ApprovalCard, DecidedLine, ReviewCard } from '../components/approval';
  import { itemQuestion, itemSource, loadItems } from '../components/approval/items';
  import type { InboxItem } from '../components/approval/types';
  import { votes } from '../components/approval/votes.svelte';
  import { announce } from '../components/ui/live.svelte';
  import Button from '../components/ui/Button.svelte';
  import EmptyState from '../components/ui/EmptyState.svelte';
  import Page from '../components/ui/Page.svelte';
  import { prefersReducedMotion } from '../components/ui/layer';
  import Verbatim from '../components/Verbatim.svelte';
  import { api, ApiError } from '../lib/api/client';
  import type { Approval, Choice, List } from '../lib/api/types.gen';
  import { cardCopy, decidable, expiryOf, isMulti, isOpen, originOf, requesterOf, segText, timeOfDay } from '../lib/cardcopy';
  import { date, relTime } from '../lib/format';
  import { choiceDone } from '../lib/labels';
  import { router } from '../lib/router.svelte';
  import { events } from '../lib/sse.svelte';
  import { clock } from '../lib/stores/clock.svelte';
  import { approvals, session } from '../lib/stores/session.svelte';

  let { params }: { params: Record<string, string> } = $props();

  type Row = { key: string; created: string; approval?: Approval; item?: InboxItem };

  const done = $derived(router.query.get('tab') === 'done');
  const me = $derived(session.me ? { id: session.me.user.id, role: session.me.user.role } : undefined);

  let items: InboxItem[] = $state([]);
  let loaded = $state(false);
  let failed = $state(false);
  let slow = $state(false);
  let expanded: string | null = $state(null);
  let cursor: string | null = $state(null);
  // The expanded card takes A/S/D. When it changed without the person acting
  // (decided elsewhere, a resync), keys stay off until they move or click, so
  // a key press never decides a card they didn't see.
  let armed = $state(true);
  let listEl: HTMLElement | undefined = $state();
  let emptyEl: HTMLElement | undefined = $state();
  // The key that made the last decision, until released: key repeat must not decide the next card too.
  let held = '';
  // Where the open row sat among "For you", so the one after it opens once it is decided and gone.
  let openIndex = 0;

  // A decided row folds away (§3.6); without animation support or with reduced motion it just goes.
  function collapse(node: Element) {
    if (typeof (node as HTMLElement).animate !== 'function' || prefersReducedMotion()) return { duration: 0 };
    return slide(node, { duration: 160, easing: cubicOut });
  }

  const byCreated = (x: Row, y: Row) => x.created.localeCompare(y.created);
  const rowOf = (a: Approval): Row => ({ key: `a:${a.id}`, created: a.created_at, approval: a });
  const waiting = $derived(approvals.pending.filter(isOpen));
  // Requests to act come before reviews (§8.8); each oldest first.
  const forYou = $derived([
    ...waiting
      .filter((a) => decidable(a, me, votes.byId[a.id]))
      .map(rowOf)
      .sort(byCreated),
    ...items.map((i): Row => ({ key: `i:${i.id}`, created: i.created_at, item: i })).sort(byCreated),
  ]);
  const others = $derived(
    waiting
      .filter((a) => !decidable(a, me, votes.byId[a.id]))
      .map(rowOf)
      .sort(byCreated),
  );
  const rows = $derived([...forYou, ...others]);
  // Counts only what waits on the viewer, so the tab never shows a number above "Nothing needs your approval".
  const count = $derived(forYou.length);

  async function loadWaiting() {
    failed = false;
    const t = setTimeout(() => (slow = true), 400);
    try {
      const [p, k, its] = await Promise.all([
        api.get<List<Approval>>('/api/approvals?status=pending&limit=200'),
        api.get<List<Approval>>('/api/approvals?status=parked&limit=200'),
        loadItems(),
      ]);
      approvals.pending = [...new Map([...(p.items ?? []), ...(k.items ?? [])].filter(Boolean).map((a) => [a.id, a])).values()];
      items = its;
      loaded = true;
    } catch {
      failed = true;
    } finally {
      clearTimeout(t);
      slow = false;
    }
  }

  $effect(() => {
    void loadWaiting();
    // Reviews, new senders and new group chats change without an approval event.
    const fresh = () => void loadItems().then((its) => (items = its));
    const offs = [events.onResync(() => void loadWaiting()), events.on('inbox.changed', fresh), events.on('mcp.pin_pending', fresh)];
    return () => offs.forEach((f) => f());
  });

  // Who may decide a multi-approver request depends on its votes.
  $effect(() => {
    const ids = waiting.filter(isMulti).map((a) => a.id);
    untrack(() => ids.filter((id) => !(id in votes.byId)).forEach((id) => votes.load(id)));
    if (!ids.length) return;
    const t = setInterval(() => ids.forEach((id) => votes.load(id)), 15_000);
    return () => clearInterval(t);
  });

  // /approvals/:id opens that request in the list; one that is no longer waiting is looked up.
  let target: Approval | null = $state(null);
  let missing = $state(false);
  let placed = '';
  $effect(() => {
    const id = params.id;
    if (!id || !loaded || placed === id) return;
    placed = id;
    target = null;
    missing = false;
    if (rows.some((r) => r.key === `a:${id}`)) {
      void expand(`a:${id}`, { focus: false, scroll: 'start' });
      return;
    }
    api.get<Approval>(`/api/approvals/${encodeURIComponent(id)}`).then(
      (a) => (isOpen(a) ? approvals.upsert(a) : (target = a)),
      (e) => (missing = e instanceof ApiError && (e.status === 404 || e.status === 403)),
    );
  });

  // Keeps one row expanded: the first one for you, unless the person chose another.
  $effect(() => {
    if (!loaded) return;
    if (expanded && rows.some((r) => r.key === expanded)) return;
    // The row that took the decided one's place opens; the event stream can remove it before our own reply lands.
    const was = expanded;
    expanded = forYou[Math.min(openIndex, forYou.length - 1)]?.key ?? null;
    if (was) armed = false;
  });

  async function expand(key: string, opts: { focus?: boolean; scroll?: ScrollLogicalPosition } = {}) {
    expanded = key;
    openIndex = Math.max(0, forYou.findIndex((r) => r.key === key));
    cursor = key;
    armed = true;
    await tick();
    const el = listEl?.querySelector<HTMLElement>(`[data-row="${CSS.escape(key)}"]`);
    el?.scrollIntoView?.({ block: opts.scroll ?? 'nearest' });
    if (opts.focus !== false) el?.querySelector<HTMLElement>('article')?.focus();
  }

  function rowEl(key: string) {
    const el = listEl?.querySelector<HTMLElement>(`[data-row="${CSS.escape(key)}"]`);
    return el?.querySelector<HTMLElement>('article') ?? el?.querySelector<HTMLElement>('button[aria-expanded]');
  }

  function move(d: 1 | -1) {
    if (!rows.length) return;
    const at = rows.findIndex((r) => r.key === (cursor ?? expanded));
    const next = rows[Math.min(rows.length - 1, Math.max(0, at < 0 ? 0 : at + d))];
    cursor = next.key;
    armed = true;
    const el = rowEl(next.key);
    el?.scrollIntoView?.({ block: 'nearest' });
    el?.focus();
  }

  // After the person's own decision the next request for them opens and takes
  // focus (the card, not a button, so a held Enter can't decide it too).
  async function decided(key: string, outcome: string) {
    // The store may already have dropped the decided row; then its old place is openIndex.
    const found = forYou.findIndex((r) => r.key === key);
    const at = found >= 0 ? found : openIndex;
    const rest = forYou.filter((r) => r.key !== key);
    const next = rest[Math.min(at, rest.length - 1)];
    if (next) {
      const q = next.approval ? segText(cardCopy(next.approval).question) : itemQuestion(next.item!);
      announce(`${outcome}. Next: ${q}`);
      await expand(next.key);
    } else {
      expanded = null;
      announce(`${outcome}. Nothing else is waiting.`);
      await tick();
      (listEl?.querySelector<HTMLElement>('[data-row] button[aria-expanded]') ?? emptyEl)?.focus();
    }
  }

  function onApprovalResolved(key: string, choice: Choice, result: Approval) {
    void decided(key, choice === 'deny' && isMulti(result) && requesterOf(result) === me?.id ? 'Cancelled' : choiceDone[choice]);
  }

  const itemOutcome: Record<string, string> = { approve: 'Approved', turn_off: 'Turned off', guest: 'Added as a guest', link: 'Linked', block: 'Blocked', turn_on: 'Turned on', ignore: 'Ignored', deny: 'Denied' };

  function onItemResolved(key: string, choice: string) {
    void decided(key, itemOutcome[choice] ?? 'Done');
    items = items.filter((i) => `i:${i.id}` !== key);
  }

  const shortcut: Record<string, Choice> = { a: 'allow_once', s: 'allow_session', d: 'deny' };

  function onkeydown(e: KeyboardEvent) {
    if (done || e.metaKey || e.ctrlKey || e.altKey || e.defaultPrevented) return;
    if (e.target instanceof Element && e.target.closest('input, textarea, select, [contenteditable], dialog')) return;
    const inList = e.target instanceof Element && !!listEl?.contains(e.target);
    if (e.key === 'j' || e.key === 'k' || (inList && (e.key === 'ArrowDown' || e.key === 'ArrowUp'))) {
      e.preventDefault();
      move(e.key === 'j' || e.key === 'ArrowDown' ? 1 : -1);
      return;
    }
    const c = shortcut[e.key.toLowerCase()];
    if (!c || e.repeat || held === e.key.toLowerCase()) return;
    if (!expanded || !armed || (cursor && cursor !== expanded)) return;
    const b = listEl?.querySelector<HTMLButtonElement>(`[data-row="${CSS.escape(expanded)}"] button[data-choice="${c}"]`);
    if (!b) return;
    e.preventDefault();
    held = e.key.toLowerCase();
    b.click();
  }

  function rowMeta(r: Row): { text: string; warn?: boolean }[] {
    if (r.item) return [{ text: itemSource(r.item) }, { text: relTime(r.item.created_at, { now: clock.now }) }];
    const a = r.approval!;
    const out: { text: string; warn?: boolean }[] = [];
    const vv = votes.byId[a.id];
    if (isMulti(a) && vv) out.push({ text: `${vv.approvals} of ${vv.required} approvals` });
    else {
      const o = originOf(a);
      const src = o.title ?? o.app;
      if (src) out.push({ text: src });
    }
    const exp = expiryOf(a, clock.now);
    const at = date(a.card?.expires) ?? date(a.expires_at);
    if (a.status === 'parked') out.push({ text: `Waiting since ${timeOfDay(a.created_at)}` });
    else if (exp && at && at.getTime() - clock.now < 15 * 60_000) out.push({ text: exp.text, warn: true });
    else out.push({ text: relTime(a.created_at, { now: clock.now }) });
    return out;
  }

  // The Done tab: decisions of the last 30 days, grouped by day.
  let recent: Approval[] = $state([]);
  let recentLoaded = $state(false);
  let recentFailed = $state(false);
  let showing: string | null = $state(null);

  async function loadRecent() {
    recentFailed = false;
    try {
      const r = await api.get<List<Approval>>('/api/approvals?status=recent&limit=100');
      const since = Date.now() - 30 * 86_400_000;
      recent = (r.items ?? []).filter((a) => a && (date(a.decided_at ?? a.created_at)?.getTime() ?? 0) >= since);
      recentLoaded = true;
      // Decided lines name every voter, and the names come with the vote state.
      for (const a of recent) if (isMulti(a) && !(a.id in votes.byId)) void votes.load(a.id);
    } catch {
      recentFailed = true;
    }
  }

  $effect(() => {
    if (!done) return;
    void loadRecent();
    const off = events.on('approval.resolved', () => void loadRecent());
    return off;
  });

  const groups = $derived.by(() => {
    const today = new Date(clock.now).toDateString();
    const yesterday = new Date(clock.now - 86_400_000).toDateString();
    const out: { label: string; list: Approval[] }[] = [
      { label: 'Today', list: [] },
      { label: 'Yesterday', list: [] },
      { label: 'Earlier', list: [] },
    ];
    for (const a of recent) {
      const day = (date(a.decided_at) ?? date(a.created_at))?.toDateString();
      out[day === today ? 0 : day === yesterday ? 1 : 2].list.push(a);
    }
    return out.filter((g) => g.list.length);
  });

  const tabs = $derived([
    { label: 'Waiting', href: '/approvals', count },
    { label: 'Done', href: '/approvals?tab=done' },
  ]);
</script>

<svelte:window {onkeydown} onkeyup={(e) => e.key.toLowerCase() === held && (held = '')} onblur={() => (held = '')} />

{#snippet chatLink(a: Approval)}
  {@const o = originOf(a)}
  {#if a.session_id}
    {#if o.title}From{' '}{/if}<a
      class="link -my-0.5 inline-flex min-h-6 items-center pointer-coarse:-my-3 pointer-coarse:min-h-11"
      href="/chat/{encodeURIComponent(a.session_id)}"
      onclick={router.link}>{#if o.title}<span data-copy-ignore>“<Verbatim text={o.title} />”</span>{:else}Open the chat{/if}</a
    >
  {/if}
{/snippet}

{#snippet list(section: Row[])}
  <ul class="mt-2" onfocusin={(e) => (cursor = (e.target as Element).closest<HTMLElement>('[data-row]')?.dataset.row ?? cursor)}>
    {#each section as r (r.key)}
      <li data-row={r.key} class="scroll-mt-6 border-b border-border last:border-b-0" out:collapse>
        {#if expanded === r.key}
          <div class="py-3">
            {#if r.approval}
              <ApprovalCard approval={r.approval} mode="inbox" level={3} onresolve={(c, _n, res) => onApprovalResolved(r.key, c, res)} />
            {:else if r.item}
              <ReviewCard item={r.item} onresolve={(c) => onItemResolved(r.key, c)} />
            {/if}
          </div>
        {:else}
          {@const q = r.approval ? segText(cardCopy(r.approval).question) : itemQuestion(r.item!)}
          <button
            type="button"
            class="-mx-2 flex min-h-10 w-[calc(100%+1rem)] cursor-pointer items-center gap-3 rounded-md px-2 py-2 text-left hover:bg-subtle focus-visible:outline-offset-[-2px] max-sm:flex-col max-sm:items-start max-sm:gap-0.5 pointer-coarse:min-h-12"
            aria-expanded="false"
            onclick={() => expand(r.key)}
          >
            <span class="w-full min-w-0 flex-1 truncate text-body font-medium text-text" title={q} data-copy-ignore={r.approval ? true : undefined}>{q}</span>
            <span class="flex shrink-0 gap-1 text-small text-muted tabular-nums">
              {#each rowMeta(r) as m, i (i)}{#if i > 0}<span aria-hidden="true">·</span>{/if}<span class={m.warn ? 'text-warning-text' : ''}>{m.text}</span>{/each}
            </span>
          </button>
        {/if}
      </li>
    {/each}
  </ul>
{/snippet}

<Page title="Approvals" {tabs} currentTab={done ? '/approvals?tab=done' : '/approvals'}>
  {#if done}
    {#if recentFailed}
      <EmptyState text="This page didn't load. Check your connection and try again." action={{ label: 'Try again', onclick: loadRecent, variant: 'secondary' }} />
    {:else if recentLoaded && !groups.length}
      <EmptyState text="No decisions in the last 30 days." />
    {:else}
      {#each groups as g, gi (g.label)}
        <section class={gi > 0 ? 'mt-10' : ''} aria-labelledby="done-{gi}">
          <h2 id="done-{gi}" class="section-title">{g.label}</h2>
          <ul class="mt-2">
            {#each g.list as a (a.id)}
              <li class="border-b border-border py-3 last:border-b-0">
                <DecidedLine approval={a} list expanded={showing === a.id} onshow={() => (showing = showing === a.id ? null : a.id)} />
                <p class="mt-1 pl-5.5 text-small text-muted">{@render chatLink(a)}</p>
                {#if showing === a.id}
                  <div class="mt-3"><ApprovalCard approval={a} mode="readonly" level={3} /></div>
                {/if}
              </li>
            {/each}
          </ul>
        </section>
      {/each}
    {/if}
  {:else}
    {#if missing || target}
      <div class="mb-10">
        <EmptyState text="This request was already decided or doesn't exist." />
        {#if target}<div class="mt-2"><DecidedLine approval={target} list /></div>{/if}
        <Button class="mt-4" href="/approvals">Go to Approvals</Button>
      </div>
    {/if}
    {#if failed}
      <EmptyState text="This page didn't load. Check your connection and try again." action={{ label: 'Try again', onclick: loadWaiting, variant: 'secondary' }} />
    {:else if !loaded}
      {#if slow}<p class="text-body text-muted">Loading…</p>{/if}
    {:else}
      <div bind:this={listEl}>
        {#if forYou.length}
          <section aria-labelledby="for-you">
            <h2 id="for-you" class="section-title">For you</h2>
            {@render list(forYou)}
          </section>
        {:else}
          <div bind:this={emptyEl} tabindex="-1" class="outline-offset-2">
            <EmptyState text="Nothing needs your approval." more="When Bothy needs someone's OK, it shows up here." />
          </div>
        {/if}
        {#if others.length}
          <section class="mt-10" aria-labelledby="for-others">
            <h2 id="for-others" class="section-title">Waiting for others</h2>
            {@render list(others)}
          </section>
        {/if}
      </div>
    {/if}
  {/if}
</Page>
