<script lang="ts">
  import { tick, untrack, type Component, type Snippet } from 'svelte';
  import { router } from '../../lib/router.svelte';
  import { session } from '../../lib/stores/session.svelte';
  import { shell } from '../../lib/stores/ui.svelte';
  import { attention } from './attention.svelte';
  import BannerHost from './BannerHost.svelte';
  import ChatDialogs from './ChatDialogs.svelte';
  import { chats } from './chats.svelte';
  import ConnectionStatus from './ConnectionStatus.svelte';
  import { shortcutFor } from './keys';
  import NavDrawer from './NavDrawer.svelte';
  import PauseDialog from './PauseDialog.svelte';
  import SearchDialog from './SearchDialog.svelte';
  import ShortcutsDialog from './ShortcutsDialog.svelte';
  import Sidebar from './Sidebar.svelte';
  import TopBar from './TopBar.svelte';
  import UnlockFallback from './UnlockFallback.svelte';

  // The app frame (docs/design/ui.md §6.1): no app-level header, the document
  // scrolls, the sidebar is sticky with its own scroll.
  let { children }: { children: Snippet } = $props();

  // C9 owns the real unlock screen; until it lands the shell's fallback stands in.
  const owned = Object.values(import.meta.glob<Component<{ onunlocked?: () => void }>>('../../screens/settings/security/UnlockScreen.svelte', { eager: true, import: 'default' }));
  const Unlock = owned[0] ?? UnlockFallback;

  const sealed = $derived(session.isAdmin && session.banners.some((b) => b.code === 'vault_sealed'));
  const inSettings = $derived(router.path === '/settings' || router.path.startsWith('/settings/'));
  // Locked keys stop the working areas; Settings stays open so an admin can still look around.
  const blocking = $derived(sealed && (shell.unlock || !inSettings));

  function unlocked() {
    shell.unlock = false;
    session.banners = session.banners.filter((b) => b.code !== 'vault_sealed');
  }

  $effect(() => {
    if (!sealed) shell.unlock = false;
  });

  $effect(() => {
    void chats.load();
    void chats.loadRunning();
    const offs = [chats.listen(), attention.listen()];
    return () => {
      offs.forEach((f) => f());
      chats.reset();
    };
  });

  $effect(() => {
    if (session.isAdmin) void attention.refresh();
  });

  $effect(() => {
    const p = router.path;
    if (!p.startsWith('/settings')) shell.back = p + untrack(() => router.search);
  });

  // After a route change focus moves to the page's h1, or to the composer in a
  // chat (§16), so screen readers start at the new content. The first load
  // leaves focus alone, so "Skip to content" stays the first stop.
  let first = true;
  $effect(() => {
    const path = router.path;
    if (first) {
      first = false;
      return;
    }
    untrack(() => {
      if (!router.popped && !router.hash) window.scrollTo(0, 0);
      void settle(path);
    });
  });

  async function settle(path: string) {
    const chat = path.startsWith('/chat');
    for (let i = 0; i < 30 && path === router.path; i++) {
      await tick();
      if (document.querySelector('dialog[open]')) return;
      // A chat's composer may render after its header, so the h1 stands in only once it has had time.
      const target = (chat && document.querySelector<HTMLElement>('main textarea')) || (!chat || i >= 10 ? document.querySelector<HTMLElement>('main h1') : null);
      if (target) {
        if (target.tagName === 'H1' && !target.hasAttribute('tabindex')) target.tabIndex = -1;
        target.focus({ preventScroll: true });
        return;
      }
      await new Promise((r) => setTimeout(r, 50));
    }
  }

  function onkeydown(e: KeyboardEvent) {
    const s = shortcutFor(e);
    if (!s) return;
    e.preventDefault();
    if (s === 'search') shell.search = true;
    else if (s === 'shortcuts') shell.shortcuts = true;
    else router.navigate('/chat');
  }
</script>

<svelte:window {onkeydown} />

<a href="#main" class="sr-only rounded-md bg-surface px-3 py-2 text-body font-medium text-text focus:not-sr-only focus:fixed focus:top-2 focus:left-2 focus:z-50">Skip to content</a>

<div class="lg:flex">
  <nav aria-label="Main" class="sticky top-0 hidden h-dvh w-64 shrink-0 border-r border-border bg-sidebar lg:block">
    <Sidebar />
  </nav>
  <main id="main" tabindex="-1" class="min-h-dvh min-w-0 flex-1 outline-none">
    <TopBar />
    <ConnectionStatus />
    <BannerHost hide={blocking ? ['vault_sealed'] : []} />
    {#if blocking}
      <Unlock onunlocked={unlocked} />
    {:else}
      {@render children()}
    {/if}
  </main>
</div>

<NavDrawer />
<ChatDialogs />
<SearchDialog />
<ShortcutsDialog />
{#if session.isAdmin}<PauseDialog bind:open={shell.pause} />{/if}
