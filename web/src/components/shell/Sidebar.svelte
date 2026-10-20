<script lang="ts" module>
  // Survives the sidebar swapping to Settings and back, and the drawer closing (§4.2).
  let saved = 0;
</script>

<script lang="ts">
  import type { Attachment } from 'svelte/attachments';
  import type { IconName } from '../../lib/icons';
  import { router } from '../../lib/router.svelte';
  import { approvals, session } from '../../lib/stores/session.svelte';
  import { shell } from '../../lib/stores/ui.svelte';
  import Icon from '../Icon.svelte';
  import Dot from '../ui/Dot.svelte';
  import IconButton from '../ui/IconButton.svelte';
  import AccountMenu from './AccountMenu.svelte';
  import { attention } from './attention.svelte';
  import ChatList from './ChatList.svelte';
  import { mod } from './keys';
  import SettingsNav from './SettingsNav.svelte';

  // docs/design/ui.md §4.2. The same contents fill the pinned sidebar on wide
  // screens and the drawer on phones (`drawer` adds its close button).
  let { drawer = false }: { drawer?: boolean } = $props();

  const settings = $derived(router.path === '/settings' || router.path.startsWith('/settings/'));
  const under = (base: string) => router.path === base || router.path.startsWith(`${base}/`);
  const nav: { href: string; label: string; icon: IconName; count?: () => number }[] = [
    { href: '/approvals', label: 'Approvals', icon: 'approvals', count: () => approvals.waiting },
    { href: '/knowledge', label: 'Knowledge', icon: 'knowledge' },
    { href: '/activity', label: 'Activity', icon: 'activity' },
  ];
  // The hairline appears only once rows have scrolled under the fixed items above.
  let scrolled = $state(false);
  const keepScroll: Attachment<HTMLElement> = (el) => {
    el.scrollTop = saved;
    scrolled = el.scrollTop > 0;
    const onscroll = () => {
      saved = el.scrollTop;
      scrolled = saved > 0;
    };
    el.addEventListener('scroll', onscroll, { passive: true });
    return () => el.removeEventListener('scroll', onscroll);
  };

  const row = 'group/i flex h-9 w-full items-center gap-2.5 rounded-md px-2.5 text-left text-body font-medium text-text focus-visible:-outline-offset-2 pointer-coarse:h-11';
  const item = `${row} hover:bg-hover aria-[current=page]:bg-raised aria-[current=page]:shadow-raised`;
</script>

{#snippet lead(icon: IconName, accent = false)}<Icon name={icon} size={18} class="-ml-0.5 {accent ? 'text-accent-text' : 'text-muted group-aria-[current=page]/i:text-accent-text'}" />{/snippet}

<div class="flex h-full flex-col px-2">
  {#if settings}
    <SettingsNav {drawer} />
  {:else}
    <div class="flex h-12 shrink-0 items-center justify-between">
      <a href="/chat" onclick={router.link} class="flex h-11 items-center gap-2.5 rounded-md px-1.5 text-heading text-text focus-visible:-outline-offset-2"><img src="/icon.svg" alt="" width="28" height="28" class="size-7" />Bothy</a>
      {#if drawer}<IconButton icon="close" label="Close menu" variant="quiet" onclick={() => (shell.drawer = false)} />{/if}
    </div>
    <ul class="mt-1 shrink-0 space-y-1">
      <li>
        <!-- Raised even when not current: starting a chat is the sidebar's main action (§4.2). -->
        <a href="/chat" onclick={router.link} aria-current={router.path === '/chat' ? 'page' : undefined} class="{row} bg-raised shadow-raised">{@render lead('compose', true)}New chat</a>
      </li>
      <li>
        <button type="button" class={item} aria-haspopup="dialog" onclick={() => (shell.search = true)}>
          {@render lead('search')}<span class="flex-1">Search</span><span class="hidden text-caption text-muted pointer-fine:inline" aria-hidden="true">{mod()}K</span>
        </button>
      </li>
    </ul>
    <ul class="shrink-0">
      {#each nav as n (n.href)}
        {@const count = n.count?.() ?? 0}
        <li>
          <a href={n.href} onclick={router.link} aria-current={under(n.href) ? 'page' : undefined} class={item}>
            {@render lead(n.icon)}<span class="flex-1">{n.label}</span>
            {#if count}<span class="inline-flex items-center gap-1.5 text-small font-medium text-warning-text tabular-nums"><Dot size={6} label="" />{count}<span class="sr-only">{' waiting'}</span></span>{/if}
          </a>
        </li>
      {/each}
    </ul>
    <div class="-mx-2 mt-2 min-h-0 flex-1 overflow-y-auto border-t px-2 pb-3 {scrolled ? 'border-border' : 'border-transparent'}" {@attach keepScroll}>
      <ChatList />
    </div>
    <div class="-mx-2 shrink-0 border-t border-border px-2 py-2">
      {#if session.isAdmin}
        <a href="/settings" onclick={router.link} class={item}>
          {@render lead('settings')}<span class="flex-1">Settings</span>
          {#if attention.any}<Dot />{/if}
        </a>
      {/if}
      <AccountMenu />
    </div>
  {/if}
</div>
