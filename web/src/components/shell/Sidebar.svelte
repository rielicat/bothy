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
  import Count from '../ui/Count.svelte';
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
  const nav: { href: string; label: string; count?: () => number }[] = [
    { href: '/approvals', label: 'Approvals', count: () => approvals.waiting },
    { href: '/knowledge', label: 'Knowledge' },
    { href: '/activity', label: 'Activity' },
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

  const item =
    'flex h-8 w-full items-center gap-2 rounded-md px-2 text-left text-body font-medium text-muted hover:bg-hover hover:text-text focus-visible:-outline-offset-2 aria-[current=page]:bg-selected aria-[current=page]:text-text pointer-coarse:h-11';
</script>

{#snippet lead(icon: IconName)}<Icon name={icon} class="-ml-0.5" />{/snippet}

<div class="flex h-full flex-col px-2">
  {#if settings}
    <SettingsNav {drawer} />
  {:else}
    <div class="flex h-11 shrink-0 items-center justify-between">
      <a href="/chat" onclick={router.link} class="flex h-11 items-center gap-2 rounded-md px-2 text-heading text-text focus-visible:-outline-offset-2"><img src="/icon.svg" alt="" width="24" height="24" class="size-6" />Bothy</a>
      {#if drawer}<IconButton icon="close" label="Close menu" variant="quiet" onclick={() => (shell.drawer = false)} />{/if}
    </div>
    <ul class="shrink-0">
      <li>
        <a href="/chat" onclick={router.link} aria-current={router.path === '/chat' ? 'page' : undefined} class={item}>{@render lead('plus')}New chat</a>
      </li>
      <li>
        <button type="button" class={item} aria-haspopup="dialog" onclick={() => (shell.search = true)}>
          {@render lead('search')}<span class="flex-1">Search</span><span class="hidden text-caption text-faint pointer-fine:inline" aria-hidden="true">{mod()}K</span>
        </button>
      </li>
    </ul>
    <ul class="mt-4 shrink-0">
      {#each nav as n (n.href)}
        {@const count = n.count?.() ?? 0}
        <li>
          <a href={n.href} onclick={router.link} aria-current={under(n.href) ? 'page' : undefined} class={item}>
            <span class="flex-1">{n.label}</span>
            {#if count}<Count n={count} /><span class="sr-only">{' waiting'}</span>{/if}
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
          <span class="flex-1">Settings</span>
          {#if attention.any}<Dot />{/if}
        </a>
      {/if}
      <AccountMenu />
    </div>
  {/if}
</div>
