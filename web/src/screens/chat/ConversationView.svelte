<script module lang="ts">
  // Refine belongs to the chat (§7.5). The server keeps each chat's choice;
  // this remembers it between pages until the chat's detail reloads.
  const refineByChat = new Map<string, boolean>();
</script>

<script lang="ts">
  import { tick, untrack } from 'svelte';
  import ActivityGroup from '../../components/chat/ActivityGroup.svelte';
  import ChatDetails from '../../components/chat/ChatDetails.svelte';
  import ChatNotice from '../../components/chat/ChatNotice.svelte';
  import { linkCitations } from '../../components/chat/citations';
  import DecidedLine from '../../components/chat/DecidedLine.svelte';
  import InlineApproval from '../../components/chat/InlineApproval.svelte';
  import ModelPicker from '../../components/chat/ModelPicker.svelte';
  import { models, providerName } from '../../components/chat/models.svelte';
  import NewChat from '../../components/chat/NewChat.svelte';
  import { appName, people } from '../../components/chat/people.svelte';
  import PinnedDrawer from '../../components/chat/PinnedDrawer.svelte';
  import { stepOf, type StepView } from '../../components/chat/steps';
  import Icon from '../../components/Icon.svelte';
  import Markdown from '../../components/Markdown.svelte';
  import Button from '../../components/ui/Button.svelte';
  import Dialog from '../../components/ui/Dialog.svelte';
  import Disclosure from '../../components/ui/Disclosure.svelte';
  import FileToken from '../../components/ui/FileToken.svelte';
  import IconButton from '../../components/ui/IconButton.svelte';
  import { finePointer } from '../../components/ui/layer';
  import { announce } from '../../components/ui/live.svelte';
  import Menu, { type MenuItem } from '../../components/ui/Menu.svelte';
  import Spinner from '../../components/ui/Spinner.svelte';
  import { toast } from '../../components/ui/toast.svelte';
  import { tooltip } from '../../components/ui/tooltip';
  import { fill, prefix, type Phrase } from '../../lib/activity';
  import { api, ApiError } from '../../lib/api/client';
  import type { Approval, ChannelIdentity, RefineEstimate, SandboxInfo, Session, SubmitResult } from '../../lib/api/types.gen';
  import { clock, estimate, fullTime, money } from '../../lib/format';
  import { router } from '../../lib/router.svelte';
  import { session as auth } from '../../lib/stores/session.svelte';
  import { topBarActions } from '../../lib/stores/ui.svelte';
  import { buildBlocks, type Block } from '../../lib/transcript';
  import Composer from './Composer.svelte';
  import { Conversation } from './conversation.svelte';

  let { id, oncreated }: { id: string; oncreated?: (id: string) => void } = $props();

  // The parent keeps this component while a new chat gets its id, so the id is read once.
  const conv = new Conversation(untrack(() => id));
  let root: HTMLElement | undefined = $state();
  let composer: Composer | undefined = $state();
  let atBottom = $state(true);
  let scrolled = $state(false);
  let editing = $state(false);
  let titleButton: HTMLButtonElement | undefined = $state();
  let title = $state('');
  let renameError = $state('');
  let pinsOpen = $state(false);
  let pinAdding = $state(false);
  let detailsOpen = $state(false);
  let detailsRun = $state('');
  let cleanOpen = $state(false);
  let deleteOpen = $state(false);
  let busy = $state(false);
  let dialogError = $state('');
  let sendError = $state('');
  let dragging = $state(false);
  let pendingModel = $state('');
  let refine = $state(untrack(() => refineByChat.get(id)) ?? models.refine?.enabled ?? false);
  let est: RefineEstimate | null = $state(null);
  const chosen = (v: string | undefined) => (v === 'on' ? true : v === 'off' ? false : undefined);
  let senders: Record<string, string> = $state({});
  let creating: Promise<string> | null = null;
  let liveSays = '';

  const s = $derived(conv.session);
  const exposed = $derived(((s?.taint ?? 0) & 3) === 3);
  const space = $derived(s?.principal_id.startsWith('spc_') ? auth.principals.find((p) => p.id === s.principal_id) : undefined);
  const firstText = $derived(conv.items.find((i) => i.kind === 'user')?.text ?? '');
  const chatTitle = $derived(s?.title || firstText.slice(0, 48));
  const where = $derived(
    s && ['telegram', 'slack', 'discord'].includes(s.origin) ? (space ? `In ${space.display_name} on ${appName[s.origin]}` : appName[s.origin]) : '',
  );
  const blocks = $derived(buildBlocks(conv.items));
  const liveRun = $derived(conv.running && conv.live ? conv.live.id : '');
  let codeAvailable: boolean | null = $state(null);
  const noCode = $derived(auth.sandbox === false || codeAvailable === false);
  const isCode = (st: StepView) => (st.item.tool === 'shell' || st.item.tool === 'python') && st.state === 'failed';
  const steps = $derived.by(() => {
    const ctx = { byCall: conv.byCall, records: conv.records, liveRun };
    const out = new Map<string, StepView[]>();
    for (const b of blocks) {
      if (b.kind !== 'steps') continue;
      const views = b.tools.map((t) => stepOf(t, ctx));
      // Without a place to run code the command never ran, so "finished with errors" would not be true.
      out.set(b.key, noCode ? views.map((st) => (isCode(st) ? { ...st, phrases: { ...st.phrases, failed: prefix("Couldn't ", st.phrases.ask) } } : st)) : views);
    }
    return out;
  });
  const shownApprovals = $derived(new Set([...steps.values()].flat().map((st) => st.approval?.id)));
  const strayApprovals = $derived(conv.approvals.filter((a) => (a.status === 'pending' || a.status === 'parked') && !shownApprovals.has(a.id)));
  // Reply actions go under the last message of each turn; Try again only under the latest.
  // A turn that ends in steps rather than words has no reply to act on.
  const lastOfRun = $derived.by(() => {
    const out = new Map<string, Block>();
    for (const b of blocks) {
      const run = b.kind === 'assistant' ? b.msg.run_id : b.kind === 'steps' ? b.runId : undefined;
      if (run) out.set(run, b);
    }
    return new Set([...out.values()].filter((b) => b.kind === 'assistant' && b.text).map((b) => b.key));
  });
  const latestReply = $derived([...blocks].reverse().find((b) => b.kind === 'assistant' && b.text)?.key ?? '');
  const lastUser = $derived([...conv.items].reverse().find((i) => i.kind === 'user'));
  const working = $derived(
    !!conv.live && conv.running && !conv.live.text && !conv.live.tools.length && !conv.messages.some((m) => m.role === 'assistant' && m.run_id === conv.live?.id),
  );
  const exposedAfter = $derived.by(() => {
    if (!exposed) return '';
    const hit = blocks.find((b) => b.kind === 'steps' && b.tools.some((t) => t.result?.untrusted || t.live?.untrusted));
    return hit?.key ?? blocks.find((b) => b.kind === 'user')?.key ?? '';
  });
  // A command that failed because this server can't run code gets one plain line (§7.6).
  const failedCode = $derived.by(() => {
    const out = new Map<string, Phrase>();
    for (const [k, ss] of steps) {
      const st = ss.find(isCode);
      if (st) out.set(k, [...fill("Bothy can't run code on this server, so it couldn't ", {}), ...st.phrases.ask, { k: 'text', v: '.' }]);
    }
    return out;
  });
  $effect(() => {
    if (!failedCode.size || codeAvailable !== null || auth.sandbox === false) return;
    void api
      .get<SandboxInfo>('/api/sandbox')
      .then((i) => (codeAvailable = i.status.available))
      .catch(() => (codeAvailable = true));
  });
  const codeOff = $derived(noCode ? failedCode : new Map<string, Phrase>());
  const modelValue = $derived(conv.id ? (s?.model_override ?? '') : pendingModel);
  const modelLabel = $derived(modelValue ? models.name(modelValue) : models.main ? models.name(models.main) : 'Main model');
  const starter = $derived(space?.display_name ?? people.name(s?.principal_id, { you: false }));

  const refineLine = $derived.by(() => {
    const n = est?.drafts ?? models.refine?.drafts ?? 3;
    const head = `Refine is on: Bothy writes ${n} drafts, checks them and sends the best.`;
    if (est?.usd != null) {
      const e = estimate(est.usd * 1e6);
      return `${head} ${e.charAt(0).toUpperCase()}${e.slice(1)} for this reply.`;
    }
    const m = Math.round(est?.multiplier ?? models.refine?.multiplier ?? 0);
    return m > 1 ? `${head} Costs about ${m}× a normal reply.` : `${head} Slower, and costs several times more.`;
  });

  const outcome = $derived.by((): { text: string; error?: boolean; action?: { label: string; onclick: () => void }; details?: string } | null => {
    const o = conv.outcome;
    if (!o) return null;
    const admin = auth.isAdmin;
    switch (o.status) {
      case 'stopped':
        return { text: 'You stopped Bothy.' };
      case 'loop_detected':
        return { text: 'Bothy stopped because it kept repeating the same step. Try asking in a different way.' };
      case 'budget_exceeded': {
        const run = conv.runs[o.run_id]?.run;
        if (o.budget === 'steps') return { text: `Bothy paused after ${o.steps ?? run?.steps ?? 0} steps to check in.`, action: { label: 'Keep going', onclick: () => keepGoing(o.run_id) } };
        if (o.budget === 'daily_cost') {
          const limit = auth.today?.limit_micros ? ` ${money(auth.today.limit_micros)}` : '';
          return {
            text: `Bothy has reached today's${limit} spending limit. It can start again at midnight (${auth.me?.user.timezone || 'server time'}).`,
            action: admin ? { label: 'Change limit', onclick: () => router.navigate('/settings/general') } : undefined,
          };
        }
        const limit = run?.limits.max_cost_micros ? ` ${money(run.limits.max_cost_micros)}` : '';
        return {
          text: o.budget === 'run_cost' ? `Bothy stopped because this task reached its${limit} limit.` : 'Bothy stopped because this task reached its limit.',
          action: admin && o.budget === 'run_cost' ? { label: 'Keep going', onclick: () => keepGoing(o.run_id) } : undefined,
        };
      }
      case 'failed':
      case 'interrupted': {
        const calls = conv.runs[o.run_id]?.llm_calls ?? [];
        const attempts = calls.flatMap((c) => c.attempts ?? []).filter((a) => a.err);
        const again = { label: 'Try again', onclick: retry };
        if (attempts.length && attempts.every((a) => a.kind === 'auth')) {
          const who = providerName(attempts[0].ref.provider);
          return admin
            ? { text: `${who} stopped accepting Bothy's key.`, error: true, action: { label: 'Update key', onclick: () => router.navigate('/settings/models') } }
            : { text: "Bothy can't reach its AI model right now. An admin has been told.", error: true };
        }
        if (new Set(attempts.map((a) => `${a.ref.provider}/${a.ref.model}`)).size > 1) return { text: 'No AI model could answer right now.', error: true, action: again };
        const ref = attempts[0]?.ref.provider ?? (conv.runs[o.run_id]?.run?.models?.[0] ?? models.main).split('/')[0];
        return { text: `${ref ? providerName(ref) : 'The AI model'} had a problem answering.`, error: true, action: again, details: o.error || attempts[0]?.err };
      }
    }
    return null;
  });

  let loaded = $state(false);
  let hit: Element | null = null;

  $effect(() => {
    conv.listen();
    void conv.load().then(() => (loaded = true));
    void models.load(auth.isAdmin);
    people.load();
    if (finePointer()) void tick().then(() => composer?.focus());
    const onscroll = () => measure();
    window.addEventListener('scroll', onscroll, true);
    window.addEventListener('resize', onscroll);
    return () => {
      conv.dispose();
      window.removeEventListener('scroll', onscroll, true);
      window.removeEventListener('resize', onscroll);
    };
  });

  $effect(() => {
    if (!space || !conv.id) return;
    void api
      .get<ChannelIdentity[]>('/api/identities')
      .then((list) => {
        for (const i of list ?? []) senders[`${i.channel}:${i.external_id}`] = i.display_name || i.username;
      })
      .catch(() => {});
  });

  // The chat's own choice, else Settings → Models → "Refine by default".
  $effect(() => {
    const d = chosen(conv.detail?.refine) ?? models.refine?.enabled;
    if (d !== undefined && !untrack(() => refineByChat.has(conv.id))) refine = d;
  });

  function setRefine(on: boolean) {
    refineByChat.set(conv.id, on);
    if (conv.id) void api.patch(conv.base, { refine: on ? 'on' : 'off' }).catch(() => {});
  }

  // The estimate follows the chat's recent replies, so it is asked again after each run.
  $effect(() => {
    void conv.lastRunId;
    if (!refine || !conv.id) return;
    void api
      .get<RefineEstimate>(`${conv.base}/refine-estimate`)
      .then((e) => (est = e))
      .catch(() => (est = null));
  });

  $effect(() => {
    const f = conv.finished;
    if (f && f.status === 'done' && f.run_id !== liveSays) {
      liveSays = f.run_id;
      announce('Bothy replied.');
    }
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
    hit?.classList.remove('bg-warning-subtle');
    hit = el;
    if (el) {
      atBottom = false;
      el.scrollIntoView({ block: 'center' });
      el.classList.add('bg-warning-subtle');
    } else scrollDown();
  }

  $effect(() => {
    void conv.items.length;
    void conv.live?.text.length;
    void conv.approvals.length;
    void working;
    // The composer strip grows with the Refine line and errors; keep the latest words above it.
    void refine;
    void sendError;
    if (untrack(() => atBottom)) void tick().then(scrollDown);
  });

  function scroller(): HTMLElement {
    for (let el = root?.parentElement; el && el !== document.body; el = el.parentElement) {
      const o = getComputedStyle(el).overflowY;
      if ((o === 'auto' || o === 'scroll') && el.scrollHeight > el.clientHeight) return el;
    }
    return (document.scrollingElement as HTMLElement) ?? document.documentElement;
  }

  function measure() {
    const sc = scroller();
    atBottom = sc.scrollHeight - sc.scrollTop - sc.clientHeight < 120;
    scrolled = sc.scrollTop > 0;
  }

  function scrollDown() {
    const sc = scroller();
    sc.scrollTop = sc.scrollHeight;
    measure();
  }

  function ensureSession(): Promise<string> {
    if (conv.id) return Promise.resolve(conv.id);
    creating ??= api
      .post<Session>('/api/sessions', {})
      .then(async (n) => {
        conv.adopt(n);
        refineByChat.set(n.id, refine);
        if (refine !== !!models.refine?.enabled) void api.patch(conv.base, { refine: refine ? 'on' : 'off' }).catch(() => {});
        oncreated?.(n.id);
        router.navigate(`/chat/${encodeURIComponent(n.id)}`, { replace: true });
        if (pendingModel) conv.setSession(await api.patch<Session>(conv.base, { model_override: pendingModel }).catch(() => n));
        return n.id;
      })
      .finally(() => (creating = null));
    return creating;
  }

  async function send(text: string, uploads: string[]): Promise<boolean> {
    sendError = '';
    try {
      await ensureSession();
      const r = await api.post<SubmitResult>(`${conv.base}/messages`, { text, uploads, refine });
      if (r?.queued) conv.queued = [...conv.queued, text];
      atBottom = true;
      return true;
    } catch (e) {
      sendError =
        e instanceof ApiError && e.code === 'busy'
          ? 'Bothy is busy with this chat. Stop it or wait a moment.'
          : e instanceof ApiError && e.code === 'paused'
            ? "Bothy is paused. Your message will be answered when it's resumed."
            : "Couldn't send your message. Try again.";
      return false;
    }
  }

  function retry() {
    if (lastUser?.kind === 'user' && lastUser.text) void send(lastUser.text, []);
  }

  async function stop() {
    if (conv.id) await api.post(`${conv.base}/stop`).catch(() => {});
  }

  async function keepGoing(runId: string) {
    await api.post(`/api/runs/${encodeURIComponent(runId)}/continue`).catch(() => (sendError = "Couldn't continue. Try again."));
  }

  async function setModel(v: string) {
    if (!conv.id) {
      pendingModel = v;
      return;
    }
    try {
      conv.setSession(await api.patch<Session>(conv.base, { model_override: v }));
    } catch {
      sendError = "Couldn't change the model. Try again.";
    }
  }

  async function command(cmd: string, arg: string) {
    switch (cmd) {
      case 'new':
        return router.navigate('/chat');
      case 'stop':
        return stop();
      case 'pin':
        if (!conv.id) return;
        if (arg && (await conv.addPin(arg).then(() => true, () => false))) return toast('Pinned.');
        pinAdding = !arg;
        pinsOpen = true;
        return;
      case 'model': {
        const m = models.list.find((x) => x.display.toLowerCase().includes(arg.toLowerCase()) || `${x.ref.provider}/${x.ref.model}` === arg);
        if (arg && m) return setModel(`${m.ref.provider}/${m.ref.model}`);
        return document.querySelector<HTMLElement>('[aria-label^="Model:"]')?.click();
      }
      case 'compact':
        if (conv.id) await api.post(`${conv.base}/compact`).catch(() => (sendError = "Couldn't summarise this chat. Try again."));
    }
  }

  function startRename() {
    title = chatTitle;
    renameError = '';
    editing = true;
  }

  let renaming: Promise<void> | null = null;
  function saveTitle(): Promise<void> {
    // Esc closes the field first, so a blur that follows saves nothing; Enter
    // and the blur that follows it share one request.
    if (!editing) return Promise.resolve();
    renaming ??= (async () => {
      const t = title.trim();
      try {
        if (t && t !== s?.title) conv.setSession(await api.patch<Session>(conv.base, { title: t }));
        editing = false;
      } catch {
        renameError = "Couldn't rename this chat. Try again.";
      } finally {
        renaming = null;
      }
    })();
    return renaming;
  }

  async function fork() {
    busy = true;
    dialogError = '';
    try {
      const n = await api.post<Session>(`${conv.base}/fork`);
      cleanOpen = false;
      router.navigate(`/chat/${encodeURIComponent(n.id)}`);
      toast('Started a clean chat.');
    } catch {
      dialogError = "Couldn't start a clean chat. Try again.";
    } finally {
      busy = false;
    }
  }

  async function remove() {
    busy = true;
    dialogError = '';
    try {
      await api.del(conv.base);
      deleteOpen = false;
      router.navigate('/chat');
      toast('Chat deleted.');
    } catch {
      dialogError = "Couldn't delete this chat. Try again.";
    } finally {
      busy = false;
    }
  }

  async function copy(text: string) {
    try {
      await navigator.clipboard.writeText(text);
      toast('Copied.');
    } catch {
      // No clipboard on plain http; the text stays selectable.
    }
  }

  async function decided(a?: Approval) {
    if (a) return conv.resolved(a);
    for (const p of conv.approvals.filter((x) => x.status === 'pending' || x.status === 'parked')) {
      const fresh = await api.get<Approval>(`/api/approvals/${encodeURIComponent(p.id)}`).catch(() => null);
      if (fresh) conv.resolved(fresh);
    }
  }

  function madeFiles(runId?: string) {
    if (!runId) return [];
    const out: string[] = [];
    for (const ss of steps.values()) for (const st of ss) if (st.item.runId === runId && st.item.tool === 'fs_write' && st.state === 'done' && st.input.action?.paths?.[0]) out.push(st.input.action.paths[0]);
    return [...new Set(out)];
  }

  const fileHref = (path: string) => `/api/workspace/files?principal=${encodeURIComponent(s?.principal_id ?? '')}&path=${encodeURIComponent(path)}`;

  // 24 px between turns, 16 px from a message to the reply, 12 px from steps to the text after them (§7.1).
  function gap(prev: Block | undefined, b: Block): string {
    if (!prev) return '';
    if (b.kind === 'day') return 'mt-6';
    if (prev.kind === 'day') return 'mt-2';
    if (b.kind === 'user') return 'mt-6';
    if (prev.kind === 'user') return 'mt-4';
    return 'mt-3';
  }

  const senderOf = (sender?: string) => (sender?.startsWith('web:') ? people.name(sender.slice(4)) : (sender && senders[sender]) || `Someone on ${appName[sender?.split(':')[0] ?? ''] ?? 'a chat app'}`);

  const menu: MenuItem[] = $derived([
    { label: 'Rename', onselect: startRename },
    { label: `Pinned (${conv.pins.length})`, onselect: () => ((pinAdding = false), (pinsOpen = true)) },
    { label: 'Chat details', onselect: () => ((detailsRun = ''), (detailsOpen = true)) },
    { label: 'Continue in a clean chat', onselect: () => ((dialogError = ''), (cleanOpen = true)) },
    'separator',
    { label: 'Delete chat…', danger: true, onselect: () => ((dialogError = ''), (deleteOpen = true)) },
  ]);

  // Knowledge's "Ask Bothy about …" opens /chat?prompt=…: the text waits in the composer and is never sent.
  $effect(() => {
    const p = router.query.get('prompt');
    if (!conv.id && p && composer) untrack(() => composer?.fill(p));
  });

  // Below 1024 px the shell's top bar shows the title and this menu (§4.4); the header stays for screen readers and renaming.
  $effect(() => {
    if (conv.id) return topBarActions(chatMenu);
  });

  function dragover(e: DragEvent) {
    if (!e.dataTransfer?.types.includes('Files') || auth.paused) return;
    e.preventDefault();
    dragging = true;
  }
</script>

{#snippet chatMenu()}
  <Menu items={menu} placement="bottom-end">
    {#snippet trigger(props)}<IconButton icon="more" label="Chat options" {...props} />{/snippet}
  </Menu>
{/snippet}

<svelte:head><title>{conv.id ? chatTitle || 'Chat' : 'New chat'} · Bothy</title></svelte:head>

{#snippet copyButton(text: string, at: string)}
  <button
    type="button"
    class="btn btn-quiet btn-sm btn-icon"
    aria-label="Copy"
    onclick={() => copy(text)}
    {@attach tooltip(() => (at ? `Copy · ${clock(at)}` : 'Copy'))}><Icon name="copy" /><time class="sr-only" datetime={at}>{fullTime(at)}</time></button
  >
{/snippet}

{#snippet approvalFor(st: StepView)}
  {@const a = st.approval!}
  {#if a.status === 'pending' || a.status === 'parked'}
    <div class="mt-2 mb-3"><InlineApproval approval={a} onresolved={decided} /></div>
  {:else}
    <DecidedLine approval={a} phrases={st.phrases} />
  {/if}
{/snippet}

<!-- svelte-ignore a11y_no_static_element_interactions -->
<div
  bind:this={root}
  class="relative mx-auto flex min-h-dvh w-full max-w-3xl flex-col px-6 max-lg:min-h-[calc(100dvh-3.25rem)] max-sm:px-4"
  ondragover={dragover}
  ondragleave={(e) => {
    if (!root?.contains(e.relatedTarget as Node | null)) dragging = false;
  }}
  ondrop={(e) => {
    if (!dragging) return;
    e.preventDefault();
    dragging = false;
    if (e.dataTransfer?.files.length) composer?.addFiles(e.dataTransfer.files);
  }}
>
  {#if conv.id}
    <header class="sticky top-0 z-10 flex min-h-13 items-center gap-2 border-b bg-bg py-1 {scrolled ? 'border-border' : 'border-transparent'} {editing ? 'max-lg:top-13' : 'max-lg:sr-only'}">
      <div class="min-w-0 flex-1">
        <!-- The chat title stays the page's h1 while it is being renamed. -->
        <h1 class="flex min-w-0" tabindex="-1">
          {#if editing}
            <input
              class="input -ml-3 h-8 w-[calc(100%+0.75rem)] text-heading max-sm:text-[1rem] pointer-coarse:h-11 pointer-coarse:text-[1rem]"
              aria-label="Chat title"
              bind:value={title}
              aria-invalid={!!renameError}
              aria-describedby={renameError ? 'rename-error' : undefined}
              {@attach (el) => el.focus()}
              onblur={() => void saveTitle()}
              onkeydown={async (e) => {
                if (e.key !== 'Enter' && e.key !== 'Escape') return;
                if (e.key === 'Enter') await saveTitle();
                else editing = false;
                if (!editing) void tick().then(() => titleButton?.focus());
              }}
            />
          {:else}
            <button
              bind:this={titleButton}
              type="button"
              class="-mx-1 min-h-6 min-w-0 cursor-pointer truncate rounded-sm px-1 text-left text-heading text-text hover:bg-subtle pointer-coarse:min-h-11"
              onclick={startRename}
              {@attach tooltip(() => 'Rename', { describe: true })}><span data-copy-ignore>{chatTitle || 'Chat'}</span></button
            >
          {/if}
        </h1>
        {#if editing && renameError}<p id="rename-error" class="field-error">{renameError}</p>{/if}
        {#if !editing && where}<p class="truncate text-small text-muted">{where}</p>{/if}
      </div>
      <div class="max-lg:hidden">{@render chatMenu()}</div>
    </header>
  {/if}

  <section class="flex-1 pt-4 pb-6" aria-label="Messages">
    {#if !conv.id}
      <NewChat onpick={(t) => composer?.fill(t)} />
    {:else if conv.loading}
      <p class="text-body text-muted" aria-busy="true">Loading…</p>
    {:else if conv.error}
      <ChatNotice error text="This chat didn't load. Check your connection and try again." action={{ label: 'Try again', onclick: () => conv.load() }} details={conv.error} />
    {:else}
      {#if conv.hasMore}
        <div class="mb-4 flex justify-center"><Button size="sm" variant="ghost" onclick={() => conv.loadEarlier().catch(() => {})}>Load earlier messages</Button></div>
      {/if}
      {#each blocks as b, i (b.key)}
        <div class={gap(blocks[i - 1], b)}>
          {#if b.kind === 'day'}
            <p class="text-center text-caption text-muted">{b.label}</p>
          {:else if b.kind === 'user'}
            <div id="m{b.msg.seq}" class="group flex flex-col items-end rounded-md">
              {#if space}<p class="mb-1 text-small font-medium text-muted">{senderOf(b.msg.sender)}</p>{/if}
              <div class="flex max-w-[85%] min-w-0 items-start gap-1">
                {#if b.text}
                  <div class="opacity-0 group-focus-within:opacity-100 group-hover:opacity-100 pointer-coarse:opacity-100">{@render copyButton(b.text, b.msg.created_at)}</div>
                  <div class="min-w-0 rounded-lg bg-subtle px-4 py-2.5 text-reading text-text [overflow-wrap:anywhere]"><Markdown text={b.text} {exposed} /></div>
                {/if}
              </div>
              {#if b.files.length}
                <ul class="mt-2 flex max-w-[85%] flex-wrap justify-end gap-2">
                  {#each b.files as f (f.ref)}<li class="max-w-full min-w-0"><FileToken name={f.name || f.ref.split('/').pop() || f.ref} size={f.size} href={fileHref(f.ref)} /></li>{/each}
                </ul>
              {/if}
              {#if b.steer}<p class="mt-1 text-small text-muted">Added while Bothy was working</p>{/if}
            </div>
          {:else if b.kind === 'assistant'}
            {@const files = lastOfRun.has(b.key) ? madeFiles(b.msg.run_id) : []}
            <div id="m{b.msg.seq}" class="group rounded-md">
              {#if b.thinking}
                <Disclosure label="Thought process" class="mb-2"><p class="text-small whitespace-pre-wrap text-muted" data-copy-ignore>{b.thinking}</p></Disclosure>
              {/if}
              {#if b.text}<Markdown text={linkCitations(b.text, exposed)} {exposed} />{/if}
              {#if files.length}
                <ul class="mt-3 flex flex-wrap gap-2">
                  {#each files as f (f)}<li class="max-w-full min-w-0"><FileToken name={f.split('/').pop() ?? f} href={fileHref(f)} /></li>{/each}
                </ul>
              {/if}
              {#if lastOfRun.has(b.key) && !(conv.running && b.msg.run_id === liveRun)}
                <div
                  class="mt-1 -ml-2 flex items-center gap-0.5 {b.key === latestReply
                    ? ''
                    : 'opacity-0 group-focus-within:opacity-100 group-hover:opacity-100 pointer-coarse:opacity-100'}"
                >
                  {@render copyButton(b.text, b.msg.created_at)}
                  {#if b.key === latestReply && lastUser && !conv.running && !outcome}<IconButton icon="refresh" label="Try again" size="sm" variant="quiet" onclick={retry} />{/if}
                  <IconButton
                    icon="more"
                    label="Details"
                    size="sm"
                    variant="quiet"
                    onclick={() => {
                      detailsRun = b.msg.run_id ?? '';
                      detailsOpen = true;
                    }}
                  />
                </div>
              {/if}
            </div>
          {:else if b.kind === 'stream'}
            {#if b.text}<Markdown text={linkCitations(b.text, exposed)} {exposed} />{/if}
          {:else if b.kind === 'notice'}
            <ChatNotice text={b.text} />
          {:else if b.kind === 'summary'}
            <ChatNotice text="Older messages were summarised so this chat can keep going." detailsLabel="Show summary">
              {#snippet more()}<div class="text-small"><Markdown text={b.text} {exposed} /></div>{/snippet}
            </ChatNotice>
          {:else if b.kind === 'steps'}
            <ActivityGroup steps={steps.get(b.key) ?? []} live={!!liveRun && b.runId === liveRun && i === blocks.length - 1} approval={approvalFor} />
            {#if codeOff.has(b.key)}
              <ChatNotice
                phrase={codeOff.get(b.key)}
                action={auth.isAdmin ? { label: 'How to turn it on', onclick: () => router.navigate('/settings/security') } : undefined}
              />
            {/if}
          {/if}
          {#if b.key === exposedAfter}
            <div class="mt-3">
              <ChatNotice text="From now on, Bothy will ask before sending anything out of this chat." detailsLabel="Why?">
                {#snippet more()}
                  <p class="text-small text-muted">
                    This chat has used private information and content from outside your team, like a web page. To keep that information from leaking, Bothy now asks first.
                    Continue in a clean chat to reset this.
                  </p>
                {/snippet}
              </ChatNotice>
            </div>
          {/if}
        </div>
      {/each}

      {#each strayApprovals as a (a.id)}<div class="mt-3"><InlineApproval approval={a} onresolved={decided} /></div>{/each}

      {#if conv.queued.length}<div class="mt-3"><ChatNotice text="Bothy will read this when it finishes the current step." /></div>{/if}
      {#if working}
        <p class="mt-3 flex min-h-8 items-center gap-2 text-body text-muted" role="status"><Spinner size={16} />Working…</p>
      {/if}
      {#if outcome}
        <div class="mt-3"><ChatNotice {...outcome} alert={!!outcome.error && conv.finished?.run_id === conv.outcome?.run_id} /></div>
      {:else if auth.paused && lastUser && conv.items[conv.items.length - 1] === lastUser}
        <div class="mt-3"><ChatNotice text="Bothy is paused. Your message will be answered when it's resumed." /></div>
      {/if}
    {/if}
  </section>

  <div class="sticky bottom-0 z-10 bg-bg pt-2 pb-[max(1rem,env(safe-area-inset-bottom))]">
    {#if !atBottom && conv.id}
      <div class="pointer-events-none absolute inset-x-0 -top-11 flex justify-center">
        <Button size="sm" class="pointer-events-auto" onclick={scrollDown}>Jump to latest</Button>
      </div>
    {/if}
    <Composer
      bind:this={composer}
      bind:refine
      onrefine={setRefine}
      running={conv.running}
      paused={auth.paused}
      {refineLine}
      {ensureSession}
      onsend={send}
      oncommand={command}
      onstop={stop}
    >
      {#snippet picker()}<ModelPicker value={modelValue} onchange={setModel} />{/snippet}
    </Composer>
    {#if sendError}<p class="mt-2 text-small text-danger-text" role="alert">{sendError}</p>{/if}
  </div>

  {#if dragging}
    <!-- The column is as tall as the transcript; the sticky box keeps the overlay on screen. -->
    <div class="pointer-events-none absolute inset-0 z-20 px-2">
      <div class="sticky top-2 grid h-[calc(100dvh-1rem)] place-items-center rounded-lg border border-dashed border-control bg-subtle text-body text-muted" data-floating>
        Drop files to attach
      </div>
    </div>
  {/if}
</div>

{#if s}
  <ChatDetails bind:open={detailsOpen} {conv} session={s} {starter} model={modelLabel} {refine} runId={detailsRun} />
  <PinnedDrawer bind:open={pinsOpen} bind:adding={pinAdding} {conv} />
{/if}

<Dialog bind:open={cleanOpen} title="Continue in a clean chat?" {busy}>
  <p class="text-body text-text">
    Bothy will start a new chat with a summary of this one, without the private information and outside content it has seen. This chat stays as it is.
  </p>
  {#if dialogError}<p class="mt-2 text-small text-danger-text" role="alert">{dialogError}</p>{/if}
  {#snippet footer({ close })}
    <Button onclick={close}>Cancel</Button>
    <Button variant="primary" loading={busy} onclick={fork}>Continue in a clean chat</Button>
  {/snippet}
</Dialog>

<Dialog bind:open={deleteOpen} title="Delete this chat?" initialFocus="cancel" {busy}>
  <p class="text-body text-text">The messages will be deleted. Anything Bothy already did stays done, and its record stays in Activity. This can't be undone.</p>
  {#if dialogError}<p class="mt-2 text-small text-danger-text" role="alert">{dialogError}</p>{/if}
  {#snippet footer({ close })}
    <Button data-cancel onclick={close}>Cancel</Button>
    <Button variant="danger" loading={busy} loadingLabel="Deleting…" onclick={remove}>Delete chat</Button>
  {/snippet}
</Dialog>
