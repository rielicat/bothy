<script lang="ts">
  import { tick, untrack } from 'svelte';
  import ApprovalCard from '../../components/ApprovalCard.svelte';
  import Dialog from '../../components/Dialog.svelte';
  import Icon from '../../components/Icon.svelte';
  import Markdown from '../../components/Markdown.svelte';
  import StackedBar from '../../components/StackedBar.svelte';
  import { api, ApiError } from '../../lib/api/client';
  import type { ModelInfo, Pin, RunDetail, Session, SubmitResult } from '../../lib/api/types.gen';
  import { bytes, clock as fmtClock, duration, tokens, usd } from '../../lib/format';
  import { router } from '../../lib/router.svelte';
  import { session as auth } from '../../lib/stores/session.svelte';
  import { attempt, toasts } from '../../lib/stores/ui.svelte';
  import Composer from './Composer.svelte';
  import { Conversation } from './conversation.svelte';
  import ToolRow from './ToolRow.svelte';

  let { id }: { id: string } = $props();

  // The parent keys this component by id, so capturing it once is intended.
  // svelte-ignore state_referenced_locally
  const conv = new Conversation(id);
  let scroller: HTMLDivElement;
  let stick = $state(true);
  let editing = $state(false);
  let title = $state('');
  let models: ModelInfo[] = $state([]);
  let pinsOpen = $state(false);
  let ctxOpen = $state(false);
  let trifectaOpen = $state(false);
  let newPin = $state('');
  let calls: RunDetail | null = $state(null);

  const s = $derived(conv.session);
  const taint = $derived(s?.taint ?? 0);
  const exposed = $derived((taint & 3) === 3);
  const running = $derived(!!conv.live && conv.live.status !== 'finished');
  const breakdown = $derived(conv.usage?.call.breakdown ?? conv.preview);
  const used = $derived(breakdown ? breakdown.total / Math.max(1, breakdown.window) : 0);
  const lastUserText = $derived(
    [...conv.messages]
      .reverse()
      .find((m) => m.role === 'user' && m.kind !== 'notice')
      ?.parts.find((p) => p.t === 'text')?.text ?? '',
  );
  // Continue adds steps (core.Runner.Continue), so it is offered only when
  // the steps budget stopped the run.
  const stepsStop = $derived(conv.finished?.status === 'budget_exceeded' && conv.finished.budget === 'steps');
  const statusLine = $derived.by(() => {
    const l = conv.live;
    if (!l) return '';
    switch (l.status) {
      case 'queued':
        return l.queued ? `Queued (${l.queued} ahead)` : 'Queued';
      case 'waiting_approval':
        return 'Waiting for your approval';
      case 'parked':
        return 'Parked until someone approves';
      case 'compacting':
        return 'Summarizing older messages…';
      case 'tool':
        return l.detail ? `Running ${l.detail}…` : 'Running a tool…';
      case 'finished':
        return '';
      default:
        return l.detail || 'Thinking…';
    }
  });

  let loaded = $state(false);
  let hit: Element | null = null;

  $effect(() => {
    conv.listen();
    void conv.load().then(() => (loaded = true));
    api
      .get<ModelInfo[]>('/api/models')
      .then((m) => (models = m))
      .catch(() => {});
    return () => conv.dispose();
  });

  // A search hit links to #m<seq>: on open, and again for each hit clicked
  // while this chat stays open (only the hash changes then).
  $effect(() => {
    const hash = router.hash;
    if (loaded) untrack(() => void jump(hash));
  });

  async function jump(hash: string) {
    const seq = Number(/^#m(\d+)$/.exec(hash)?.[1] ?? 0);
    if (seq) await conv.loadUntil(seq).catch(() => {});
    await tick();
    const el = seq ? document.getElementById(`m${seq}`) : null;
    hit?.classList.remove('ring-2', 'ring-warn');
    hit = el;
    if (el) {
      stick = false;
      el.scrollIntoView({ block: 'center' });
      el.classList.add('ring-2', 'ring-warn');
    } else scrollDown();
  }

  $effect(() => {
    void conv.items.length;
    void conv.live?.text.length;
    void conv.pending.length;
    if (stick) void tick().then(scrollDown);
  });

  function scrollDown() {
    if (scroller) scroller.scrollTop = scroller.scrollHeight;
  }

  function onscroll() {
    if (scroller) stick = scroller.scrollHeight - scroller.scrollTop - scroller.clientHeight < 80;
  }

  async function send(text: string, uploads: string[]): Promise<boolean> {
    try {
      const r = await api.post<SubmitResult>(`${conv.base}/messages`, { text, uploads });
      if (r?.queued) toasts.push('Queued: Bothy will read this when the current task ends.');
      stick = true;
      return true;
    } catch (e) {
      if (e instanceof ApiError && e.code === 'busy') toasts.push('Busy with this chat. Stop the run or wait a moment.', 'error');
      else toasts.error(e);
      return false;
    }
  }

  async function stop() {
    await attempt(() => api.post(`${conv.base}/stop`));
  }

  async function patch(body: { title?: string; model_override?: string }) {
    try {
      conv.setSession(await api.patch<Session>(conv.base, body));
    } catch (e) {
      toasts.error(e);
    }
  }

  async function command(cmd: string, arg: string) {
    switch (cmd) {
      case 'pin':
        if (arg) await addPin(arg);
        else pinsOpen = true;
        break;
      case 'new': {
        try {
          const n = await api.post<Session>('/api/sessions', {});
          router.navigate(`/chat/${encodeURIComponent(n.id)}`);
        } catch (e) {
          toasts.error(e);
        }
        break;
      }
      case 'model':
        await patch({ model_override: arg });
        toasts.ok(arg ? `This chat now uses ${arg}.` : 'This chat uses the default model again.');
        break;
      case 'compact':
        await attempt(() => api.post(`${conv.base}/compact`), 'Summarizing older messages…');
        break;
      case 'stop':
        await stop();
        break;
    }
  }

  async function addPin(text: string) {
    try {
      const p = await api.post<Pin>(`${conv.base}/pins`, { text });
      conv.setPins([...conv.pins, p]);
      newPin = '';
      toasts.ok('Pinned. Bothy keeps this in mind for the whole chat.');
    } catch (e) {
      toasts.error(e);
    }
  }

  async function removePin(p: Pin) {
    if (await attempt(() => api.del(`${conv.base}/pins/${p.id}`))) conv.setPins(conv.pins.filter((x) => x.id !== p.id));
  }

  async function fork() {
    try {
      const n = await api.post<Session>(`${conv.base}/fork`);
      trifectaOpen = false;
      router.navigate(`/chat/${encodeURIComponent(n.id)}`);
      toasts.ok('Started a clean fork without the untrusted content.');
    } catch (e) {
      toasts.error(e);
    }
  }

  async function cont(runId: string) {
    await attempt(() => api.post(`/api/runs/${encodeURIComponent(runId)}/continue`), 'Continuing with 10 more steps.');
  }

  async function openContext() {
    ctxOpen = true;
    const runId = conv.finished?.run_id ?? conv.live?.id ?? [...conv.messages].reverse().find((m) => m.run_id)?.run_id;
    if (runId) calls = await api.get<RunDetail>(`/api/runs/${encodeURIComponent(runId)}`).catch(() => null);
  }

  function saveTitle(e: SubmitEvent) {
    e.preventDefault();
    editing = false;
    if (title.trim() && title !== s?.title) void patch({ title: title.trim() });
  }

  const legs = [
    { bit: 1, k: 'P', name: 'Private data', desc: 'memory, your files or private tool output is in context' },
    { bit: 2, k: 'U', name: 'Untrusted content', desc: 'web pages, search results or messages from others are in context' },
    { bit: 4, k: 'S', name: 'Can send', desc: 'tools that can send data out are available' },
  ];
</script>

<div class="flex h-full min-w-0 flex-col">
  <header class="flex h-14 shrink-0 items-center gap-2 border-b border-line px-3 sm:px-4">
    <a href="/chat" onclick={router.link} class="btn btn-ghost btn-icon btn-sm lg:hidden" aria-label="All chats"><Icon name="left" /></a>
    {#if editing}
      <form class="min-w-0 flex-1" onsubmit={saveTitle}>
        <!-- svelte-ignore a11y_autofocus -->
        <input class="input h-8" aria-label="Chat title" bind:value={title} autofocus onblur={() => (editing = false)} onkeydown={(e) => e.key === 'Escape' && (editing = false)} />
      </form>
    {:else}
      <h1 class="flex min-w-0 flex-1">
        <button
          type="button"
          class="group flex min-w-0 items-center gap-1.5 truncate text-left font-semibold"
          title="Rename"
          onclick={() => {
            title = s?.title ?? '';
            editing = true;
          }}
        >
          <span class="truncate">{s?.title || 'New chat'}</span>
          <Icon name="edit" size={13} class="text-faint opacity-0 group-hover:opacity-100" />
        </button>
      </h1>
    {/if}

    <button
      type="button"
      class="flex items-center gap-1 rounded-full border px-2 py-1 text-[11px] font-semibold tracking-wider {exposed ? 'border-bad/40 bg-bad-soft text-bad' : 'border-line text-muted'}"
      aria-label="Trifecta status: {legs.filter((l) => taint & l.bit).map((l) => l.name).join(', ') || 'clean'}{exposed ? '. Exposed.' : ''}"
      title={exposed ? 'Exposed: private and untrusted data are both in context' : 'Lethal-trifecta status'}
      onclick={() => (trifectaOpen = true)}
    >
      {#each legs as l (l.k)}<span class="flex items-center gap-0.5"
          ><span class="size-1.5 rounded-full {taint & l.bit ? (exposed ? 'bg-bad' : 'bg-warn') : 'bg-line-strong'}"></span>{l.k}</span
        >{/each}
    </button>

    <select
      class="input h-8 w-auto max-w-40 py-0 text-xs max-sm:hidden"
      aria-label="Model for this chat"
      value={s?.model_override ?? ''}
      onchange={(e) => patch({ model_override: e.currentTarget.value })}
    >
      <option value="">Default model</option>
      {#each models as m, i (i)}
        <option value="{m.ref.provider}/{m.ref.model}">{m.display || m.ref.model}</option>
      {/each}
    </select>

    <button type="button" class="btn btn-ghost btn-sm" onclick={() => (pinsOpen = true)} aria-label="{conv.pins.length} pins">
      <Icon name="pin" size={15} /><span class="tabular-nums">{conv.pins.length}</span>
    </button>

    {#if breakdown}
      <button
        type="button"
        class="relative grid size-8 place-items-center rounded-full hover:bg-sunken"
        aria-label="Context: {Math.round(used * 100)}% of the window used. Open breakdown"
        title="{tokens(breakdown.total)} / {tokens(breakdown.window)} tokens{conv.usage ? ` · ${usd(conv.usage.run_cost_micros)} this run` : ''}"
        onclick={openContext}
      >
        <svg viewBox="0 0 36 36" class="size-7 -rotate-90" aria-hidden="true">
          <circle cx="18" cy="18" r="15" fill="none" stroke-width="4" class="stroke-sunken" />
          <circle
            cx="18"
            cy="18"
            r="15"
            fill="none"
            stroke-width="4"
            stroke-linecap="round"
            class={used > 0.85 ? 'stroke-bad' : used > 0.65 ? 'stroke-warn' : 'stroke-accent'}
            stroke-dasharray="{Math.max(1, used * 94.25)} 94.25"
          />
        </svg>
      </button>
    {/if}
  </header>

  <div bind:this={scroller} {onscroll} class="min-h-0 flex-1 overflow-y-auto">
    <div class="mx-auto w-full max-w-3xl space-y-4 px-3 py-6 sm:px-6" aria-live="off">
      {#if conv.loading}
        <div class="space-y-4" aria-busy="true">
          <div class="skeleton ml-auto h-10 w-2/3 rounded-2xl"></div>
          <div class="skeleton h-24 w-5/6 rounded-2xl"></div>
        </div>
      {:else if conv.error}
        <p class="alert-bad" role="alert">{conv.error}</p>
      {:else}
        {#if conv.hasMore}
          <button type="button" class="btn btn-ghost btn-sm mx-auto flex" onclick={() => conv.loadEarlier().catch((e) => toasts.error(e))}>
            Load earlier messages
          </button>
        {/if}
        {#if !conv.items.length && !running}
          <div class="py-10 text-center">
            <span class="mx-auto mb-4 grid size-12 place-items-center rounded-2xl bg-accent-soft text-accent"><Icon name="bothy" size={24} /></span>
            <h2 class="text-lg font-semibold">What can I help with?</h2>
            <p class="mt-1 text-sm text-muted">Ask anything. Bothy asks before it does anything risky.</p>
            <div class="mx-auto mt-6 grid max-w-lg gap-2 sm:grid-cols-2">
              {#each ['What can you do, and what will you ask me before doing?', 'Summarize the latest news on a topic I care about', 'Help me plan my week', 'Set up a short daily briefing for me'] as t (t)}
                <button type="button" class="card px-4 py-3 text-left text-sm hover:border-accent hover:bg-accent-soft/40" onclick={() => send(t, [])}>{t}</button>
              {/each}
            </div>
          </div>
        {/if}

        {#each conv.items as it (it.key)}
          {#if it.kind === 'user'}
            <div id="m{it.msg.seq}" class="ml-auto flex max-w-[85%] flex-col items-end gap-1 rounded-2xl">
              {#if it.steer}<span class="text-[11px] text-faint">added while working</span>{/if}
              {#if it.text}
                <div class="rounded-2xl rounded-br-md bg-accent-soft px-4 py-2.5 text-fg">
                  <Markdown text={it.text} {exposed} />
                </div>
              {/if}
              {#if it.files.length}
                <ul class="flex flex-wrap justify-end gap-1">
                  {#each it.files as f (f.ref)}<li class="chip"><Icon name="file" size={13} />{f.name || f.ref.split('/').pop()} · {bytes(f.size)}</li>{/each}
                </ul>
              {/if}
              <span class="text-[11px] text-faint">{fmtClock(it.msg.created_at)}{it.msg.sender && !it.msg.sender.startsWith('web:') ? ` · ${it.msg.sender.split(':')[0]}` : ''}</span>
            </div>
          {:else if it.kind === 'assistant'}
            <div id="m{it.msg.seq}" class="max-w-full">
              {#if it.thinking}
                <details class="mb-2 text-sm text-muted"><summary class="cursor-pointer">Thought process</summary><p class="mt-1 whitespace-pre-wrap">{it.thinking}</p></details>
              {/if}
              {#if it.text}<Markdown text={it.text} {exposed} />{/if}
            </div>
          {:else if it.kind === 'notice'}
            <div id="m{it.msg.seq}" class="flex items-start justify-center gap-2 px-4 text-center text-xs text-muted">
              <Icon name="info" size={14} class="mt-px" /><span class="max-w-prose">{it.text}</span>
            </div>
          {:else if it.kind === 'summary'}
            <details id="m{it.msg.seq}" class="rounded-xl border border-dashed border-line px-4 py-2 text-sm">
              <summary class="cursor-pointer text-muted">Earlier messages were summarized</summary>
              <div class="mt-2"><Markdown text={it.text} {exposed} /></div>
            </details>
          {:else if it.kind === 'tool'}
            <ToolRow call={it.call} result={it.result} live={it.live} runId={it.runId} />
          {:else if it.kind === 'stream'}
            <div>
              {#if it.thinking && !it.text}<p class="text-sm text-muted italic">{it.thinking.slice(-280)}</p>{/if}
              {#if it.text}<Markdown text={it.text} {exposed} class="streaming" />{/if}
            </div>
          {/if}
        {/each}

        {#each conv.pending as a (a.id)}
          <ApprovalCard approval={a} compact onresolved={(x) => conv.resolved(x)} />
        {/each}

        {#if running && statusLine}
          <p class="flex items-center gap-2 text-sm text-muted" role="status">
            <span class="flex gap-1" aria-hidden="true">
              <span class="size-1.5 animate-bounce rounded-full bg-accent"></span><span class="size-1.5 animate-bounce rounded-full bg-accent [animation-delay:120ms]"></span><span
                class="size-1.5 animate-bounce rounded-full bg-accent [animation-delay:240ms]"
              ></span>
            </span>
            {statusLine}
          </p>
        {/if}

        {#if !running && conv.finished}
          {#if conv.finished.status === 'budget_exceeded'}
            <div class="flex flex-wrap items-center gap-2">
              {#if stepsStop}
                <button type="button" class="btn btn-sm" onclick={() => cont(conv.finished!.run_id)}><Icon name="play" size={14} />Continue (+10 steps)</button>
              {:else if conv.finished.error}
                <span class="text-xs text-bad">{conv.finished.error}</span>
              {/if}
              <a href="/settings/budgets" onclick={router.link} class="text-xs text-muted underline">Budget settings</a>
            </div>
          {:else if conv.finished.status === 'failed' || conv.finished.status === 'loop_detected' || conv.finished.status === 'interrupted'}
            <div class="flex flex-wrap items-center gap-2">
              {#if conv.finished.error}<span class="text-xs text-bad">{conv.finished.error}</span>{/if}
              <button type="button" class="btn btn-sm" disabled={!lastUserText} onclick={() => send(lastUserText, [])}><Icon name="refresh" size={14} />Retry</button>
            </div>
          {/if}
        {/if}
      {/if}
    </div>
  </div>

  <div class="shrink-0 px-3 pt-2 pb-3 sm:px-6">
    <div class="relative mx-auto max-w-3xl">
      {#if !stick}
        <button type="button" class="btn btn-sm absolute -top-11 left-1/2 -translate-x-1/2 !rounded-full shadow-md" onclick={() => ((stick = true), scrollDown())}>
          <Icon name="down" size={14} />Latest
        </button>
      {/if}
      {#if exposed}
        <p class="mb-2 flex items-center gap-2 rounded-lg bg-bad-soft px-3 py-1.5 text-xs text-bad">
          <Icon name="shield" size={14} />Exposed chat: anything that sends data out needs your approval, and links are disabled.
          <button type="button" class="ml-auto font-medium underline" onclick={fork}>Fork clean</button>
        </p>
      {/if}
      <Composer sessionId={id} {running} paused={auth.paused} onsend={send} oncommand={command} onstop={stop} />
    </div>
  </div>
</div>

<Dialog bind:open={pinsOpen} title="Pinned for this chat" kind="sheet">
  <p class="mb-4 text-sm text-muted">Pins stay in Bothy's context for the whole chat, even after older messages are summarized.</p>
  <form
    class="mb-4 flex gap-2"
    onsubmit={(e) => {
      e.preventDefault();
      if (newPin.trim()) void addPin(newPin.trim());
    }}
  >
    <input class="input" placeholder="e.g. Don't send anything until I say so" aria-label="New pin" bind:value={newPin} />
    <button class="btn btn-primary" disabled={!newPin.trim()}>Pin</button>
  </form>
  {#if conv.pins.length}
    <ul class="space-y-2">
      {#each conv.pins as p (p.id)}
        <li class="card flex items-start gap-2 p-3 text-sm">
          <Icon name="pin" size={15} class="mt-0.5 text-accent" />
          <p class="min-w-0 flex-1 whitespace-pre-wrap">{p.text}</p>
          {#if p.source === 'auto'}<span class="chip">auto</span>{/if}
          <button type="button" class="btn btn-ghost btn-icon btn-sm" aria-label="Remove pin" onclick={() => removePin(p)}><Icon name="trash" size={14} /></button>
        </li>
      {/each}
    </ul>
  {:else}
    <p class="py-6 text-center text-sm text-muted">Nothing pinned yet.</p>
  {/if}
</Dialog>

<Dialog bind:open={trifectaOpen} title="What Bothy can reach in this chat">
  <ul class="space-y-3">
    {#each legs as l (l.k)}
      <li class="flex gap-3">
        <span class="mt-1 size-2.5 shrink-0 rounded-full {taint & l.bit ? (exposed ? 'bg-bad' : 'bg-warn') : 'bg-line-strong'}"></span>
        <div class="text-sm">
          <p class="font-medium">{l.k} · {l.name} <span class="font-normal text-muted">{taint & l.bit ? 'yes' : 'no'}</span></p>
          <p class="text-muted">{l.desc}</p>
        </div>
      </li>
    {/each}
  </ul>
  <p class="mt-4 rounded-lg px-3 py-2 text-sm {exposed ? 'bg-bad-soft text-bad' : 'bg-sunken text-muted'}">
    {exposed
      ? 'Private and untrusted data are both in context, so an injected instruction could try to leak data. Every send now needs your approval, session grants are paused and links are disabled. A clean fork starts over without this history.'
      : 'When private and untrusted data meet in one chat, Bothy locks down sending until you fork clean.'}
  </p>
  {#snippet footer()}
    <button type="button" class="btn" onclick={() => (trifectaOpen = false)}>Close</button>
    <button type="button" class="btn btn-primary" onclick={fork}><Icon name="fork" size={15} />Fork clean</button>
  {/snippet}
</Dialog>

<Dialog bind:open={ctxOpen} title="Context breakdown" kind="sheet">
  {#if breakdown}
    <p class="mb-3 text-sm">
      <span class="text-2xl font-semibold tabular-nums">{tokens(breakdown.total)}</span>
      <span class="text-muted">of {tokens(breakdown.window)} tokens · {Math.round(used * 100)}%</span>
    </p>
    <StackedBar b={breakdown} marker={0.7} legend />
    <p class="mt-2 text-xs text-muted">The marker shows roughly where older messages get summarized.</p>
    {#if conv.usage}
      {@const u = conv.usage.call.usage}
      {@const cacheable = u.input + u.cache_read + u.cache_write}
      <dl class="mt-5 grid grid-cols-2 gap-3 text-sm">
        <div class="card p-3"><dt class="text-xs text-muted">Cache hits</dt><dd class="text-lg font-semibold tabular-nums">{cacheable ? Math.round((u.cache_read / cacheable) * 100) : 0}%</dd></div>
        <div class="card p-3"><dt class="text-xs text-muted">This run</dt><dd class="text-lg font-semibold tabular-nums">{usd(conv.usage.run_cost_micros)}</dd></div>
      </dl>
    {/if}
    {#if calls?.llm_calls?.length}
      <h3 class="mt-6 mb-2 section-title">Model calls in the last run</h3>
      <ol class="space-y-2">
        {#each calls.llm_calls as c (c.id)}
          <li class="card p-3 text-sm">
            <div class="flex items-center gap-2">
              <span class="chip">step {c.step}</span>
              <span class="min-w-0 flex-1 truncate font-medium">{c.ref.provider}/{c.ref.model}</span>
              <span class="tabular-nums">{c.price_known ? usd(c.cost_micros) : 'price unknown'}</span>
            </div>
            <p class="mt-1 text-xs text-muted">
              {c.purpose} · {tokens(c.usage.input + c.usage.cache_read + c.usage.cache_write)} in · {tokens(c.usage.output)} out · TTFT {duration(c.ttft)} · {duration(c.latency)}
            </p>
            {#if c.attempts?.length > 1}
              <ul class="mt-1 space-y-0.5 text-xs">
                {#each c.attempts as at, i (i)}
                  <li class={at.err ? 'text-bad' : 'text-ok'}>{at.ref.provider}/{at.ref.model}: {at.err ? `${at.kind ?? 'error'}: ${at.err}` : 'ok'} · {duration(at.latency)}</li>
                {/each}
              </ul>
            {/if}
          </li>
        {/each}
      </ol>
    {/if}
  {:else}
    <p class="text-sm text-muted">No context data yet. Send a message first.</p>
  {/if}
</Dialog>

<style>
  :global(.streaming > :last-child)::after {
    content: '▍';
    margin-left: 1px;
    animation: blink 1s steps(2) infinite;
    color: var(--color-accent);
  }
  @keyframes blink {
    to {
      visibility: hidden;
    }
  }
</style>
