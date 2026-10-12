<script lang="ts">
  import type { Snippet } from 'svelte';
  import { api } from '../lib/api/client';
  import type { SandboxInfo } from '../lib/api/types.gen';
  import { usd } from '../lib/format';
  import type { IconName } from '../lib/icons';
  import { router, safeNext } from '../lib/router.svelte';
  import { events } from '../lib/sse.svelte';
  import { approvals, session } from '../lib/stores/session.svelte';
  import { attempt, theme, type ThemePref } from '../lib/stores/ui.svelte';
  import Dialog from './Dialog.svelte';
  import Icon from './Icon.svelte';

  let { children }: { children: Snippet } = $props();

  let drawer = $state(false);
  let confirmPause = $state(false);
  let sandbox: SandboxInfo | null = $state(null);

  const nav = $derived(
    [
      { href: '/chat', base: '/chat', label: 'Chat', icon: 'chat' },
      { href: '/approvals', base: '/approvals', label: 'Approvals', icon: 'bell', badge: approvals.count },
      { href: '/schedules', base: '/schedules', label: 'Schedules', icon: 'clock' },
      { href: '/memory', base: '/memory', label: 'Memory', icon: 'book' },
      { href: '/usage', base: '/usage', label: 'Usage', icon: 'chart' },
      session.isAdmin ? { href: '/audit', base: '/audit', label: 'Audit', icon: 'shield' } : null,
      { href: '/settings/general', base: '/settings', label: 'Settings', icon: 'sliders' },
    ].filter((x): x is { href: string; base: string; label: string; icon: IconName; badge?: number } => !!x),
  );

  const active = (base: string) => router.path === base || router.path.startsWith(base + '/') || (base === '/chat' && router.path.startsWith('/runs/'));

  const chip = $derived.by(() => {
    if (session.sandbox === undefined && !sandbox) return { text: 'Sandbox…', cls: 'chip', title: 'Checking the sandbox' };
    const st = sandbox?.status;
    if (st?.available ?? session.sandbox)
      return { text: `Isolated · ${st?.runtime || 'sandbox'}`, cls: 'chip chip-ok', title: st?.engine || 'Tools run in throwaway containers' };
    const refused = session.banners.some((b) => b.code === 'sandbox_not_isolated');
    return refused
      ? { text: 'Not isolated: refused', cls: 'chip chip-bad', title: st?.reason || 'The runtime failed the isolation self-test' }
      : { text: 'No sandbox: exec off', cls: 'chip chip-bad', title: st?.reason || 'Code tools are disabled until a sandbox is available' };
  });

  const spend = $derived(session.today);
  const ratio = $derived(spend && spend.limit_micros > 0 ? Math.min(1, spend.spent_micros / spend.limit_micros) : 0);

  async function loadSandbox() {
    sandbox = await api.get<SandboxInfo>('/api/sandbox').catch(() => sandbox);
  }

  $effect(() => {
    void loadSandbox();
    const offs = [events.on('sandbox.status', () => void loadSandbox()), events.onResync(() => void loadSandbox())];
    return () => offs.forEach((f) => f());
  });

  $effect(() => {
    void router.path;
    drawer = false;
  });

  async function togglePause() {
    const paused = !session.paused;
    if (await attempt(() => api.post('/api/admin/pause', { paused }), paused ? 'Bothy is paused. Every run was stopped.' : 'Bothy is running again.'))
      session.paused = paused;
    confirmPause = false;
  }

  async function restart() {
    await attempt(() => api.post('/api/admin/restart'), 'Restarting… the page reconnects by itself.');
  }

  const themes: { v: ThemePref; icon: IconName; label: string }[] = [
    { v: 'system', icon: 'monitor', label: 'System theme' },
    { v: 'light', icon: 'sun', label: 'Light theme' },
    { v: 'dark', icon: 'moon', label: 'Dark theme' },
  ];

  const initial = $derived((session.me?.user.display_name || session.me?.user.username || '?').slice(0, 1).toUpperCase());
  const safeAction = (a?: string) => safeNext(a, '');
</script>

{#snippet links()}
  <ul class="space-y-0.5">
    {#each nav as item (item.href)}
      <li>
        <a
          href={item.href}
          onclick={router.link}
          aria-current={active(item.base) ? 'page' : undefined}
          class="group flex items-center gap-3 rounded-lg px-3 py-2 text-sm text-muted transition-colors hover:bg-sunken hover:text-fg aria-[current=page]:bg-accent-soft aria-[current=page]:font-medium aria-[current=page]:text-accent"
        >
          <Icon name={item.icon} />
          <span class="flex-1">{item.label}</span>
          {#if item.badge}
            <span class="min-w-5 rounded-full bg-warn px-1.5 text-center text-xs leading-5 font-semibold text-white dark:text-bg" aria-label="{item.badge} pending">{item.badge}</span>
          {/if}
        </a>
      </li>
    {/each}
  </ul>
{/snippet}

{#snippet account()}
  <div class="space-y-3 border-t border-line p-3">
    <div class="flex items-center gap-1 rounded-lg bg-sunken p-1" role="group" aria-label="Theme">
      {#each themes as t (t.v)}
        <button
          type="button"
          class="flex flex-1 items-center justify-center rounded-md py-1 text-muted hover:text-fg aria-pressed:bg-surface aria-pressed:text-fg aria-pressed:shadow-xs"
          aria-pressed={theme.pref === t.v}
          aria-label={t.label}
          title={t.label}
          onclick={() => theme.set(t.v)}><Icon name={t.icon} size={15} /></button
        >
      {/each}
    </div>
    <div class="flex items-center gap-2">
      <span class="grid size-8 place-items-center rounded-full bg-accent-soft text-sm font-semibold text-accent" aria-hidden="true">{initial}</span>
      <div class="min-w-0 flex-1 leading-tight">
        <p class="truncate text-sm font-medium">{session.me?.user.display_name || session.me?.user.username}</p>
        <p class="text-xs text-muted capitalize">{session.me?.user.role}</p>
      </div>
      <button type="button" class="btn btn-ghost btn-icon btn-sm" aria-label="Log out" title="Log out" onclick={() => session.logout()}>
        <Icon name="logout" size={16} />
      </button>
    </div>
  </div>
{/snippet}

<a href="#main" class="sr-only z-50 rounded bg-surface px-3 py-2 focus:not-sr-only focus:fixed focus:top-2 focus:left-2">Skip to content</a>

<div class="flex h-dvh overflow-hidden">
  <nav aria-label="Main" class="hidden w-60 shrink-0 flex-col border-r border-line bg-surface md:flex">
    <a href="/chat" onclick={router.link} class="flex h-14 items-center gap-2 px-5 text-[17px] font-semibold tracking-tight">
      <span class="grid size-7 place-items-center rounded-lg bg-accent text-on-accent"><Icon name="bothy" size={17} /></span>
      Bothy
    </a>
    <div class="flex-1 overflow-y-auto px-3 py-2">{@render links()}</div>
    {@render account()}
  </nav>

  <div class="flex min-w-0 flex-1 flex-col">
    <header class="flex h-14 shrink-0 items-center gap-2 border-b border-line bg-surface/85 px-3 backdrop-blur sm:px-4">
      <button type="button" class="btn btn-ghost btn-icon md:hidden" aria-label="Open menu" onclick={() => (drawer = true)}>
        <Icon name="menu" />
      </button>
      <a href="/chat" onclick={router.link} class="font-semibold md:hidden">Bothy</a>
      <div class="flex-1"></div>
      {#if spend && spend.limit_micros > 0}
        <a
          href="/usage"
          onclick={router.link}
          class="hidden items-center gap-2 rounded-lg px-2 py-1 text-xs text-muted hover:bg-sunken sm:flex"
          title="Today's spend against your daily cap"
        >
          <span class="tabular-nums"><span class="font-medium text-fg">{usd(spend.spent_micros)}</span> / {usd(spend.limit_micros)}</span>
          <span class="h-1.5 w-16 overflow-hidden rounded-full bg-sunken" aria-hidden="true">
            <span class="block h-full rounded-full {ratio >= 0.8 ? 'bg-warn' : 'bg-accent'}" style:width="{ratio * 100}%"></span>
          </span>
        </a>
      {/if}
      <a
        href={session.isAdmin ? '/settings/sandbox' : '/usage'}
        onclick={router.link}
        class="{chip.cls} max-[420px]:hidden"
        title={chip.title}
      >
        <Icon name="box" size={13} />{chip.text}
      </a>
      <a href="/approvals" onclick={router.link} class="btn btn-ghost btn-icon relative md:hidden" aria-label="Approvals, {approvals.count} pending">
        <Icon name="bell" />
        {#if approvals.count}<span class="absolute top-1 right-1 size-2.5 rounded-full bg-warn ring-2 ring-surface"></span>{/if}
      </a>
      {#if session.isAdmin}
        <button
          type="button"
          class="btn btn-sm {session.paused ? 'btn-primary' : 'btn-ghost text-bad'}"
          onclick={() => (session.paused ? togglePause() : (confirmPause = true))}
          title={session.paused ? 'Resume Bothy' : 'Kill switch: stop every run and pause'}
        >
          <Icon name={session.paused ? 'play' : 'power'} size={15} /><span class="max-sm:sr-only">{session.paused ? 'Resume' : 'Stop all'}</span>
        </button>
      {/if}
    </header>

    {#if session.visibleBanners.length}
      <div class="shrink-0 space-y-px border-b border-line" role="region" aria-label="Notices">
        {#each session.visibleBanners as b (b.code)}
          <div
            class="flex items-start gap-2 px-4 py-2 text-sm {b.level === 'error' ? 'bg-bad-soft text-bad' : b.level === 'warn' ? 'bg-warn-soft text-warn' : 'bg-info-soft text-info'}"
          >
            <Icon name={b.level === 'info' ? 'info' : 'alert'} size={16} class="mt-0.5" />
            <p class="min-w-0 flex-1">{b.text}</p>
            {#if b.code === 'restart_required' && session.isAdmin}
              <button type="button" class="btn btn-sm" onclick={restart}>Restart now</button>
            {:else if safeAction(b.action)}
              <a href={safeAction(b.action)} onclick={router.link} class="font-medium underline underline-offset-2">Open</a>
            {/if}
            <button type="button" class="opacity-70 hover:opacity-100" aria-label="Hide notice" onclick={() => session.dismissed.push(b.code)}>
              <Icon name="x" size={15} />
            </button>
          </div>
        {/each}
      </div>
    {/if}

    <main id="main" tabindex="-1" class="min-h-0 flex-1 overflow-y-auto focus:outline-none">
      {@render children()}
    </main>
  </div>
</div>

<Dialog bind:open={drawer} title="Bothy" kind="sheet">
  <nav aria-label="Main">{@render links()}</nav>
  <div class="-mx-4 mt-6 -mb-4">{@render account()}</div>
</Dialog>

<Dialog bind:open={confirmPause} title="Stop everything?">
  <p class="text-sm text-muted">
    The kill switch stops every active run, kills every sandbox box and pauses schedules until an admin resumes. Read-only pages keep working.
  </p>
  {#snippet footer()}
    <button type="button" class="btn" onclick={() => (confirmPause = false)}>Cancel</button>
    <button type="button" class="btn btn-danger" onclick={togglePause}><Icon name="power" size={15} />Stop all runs</button>
  {/snippet}
</Dialog>
