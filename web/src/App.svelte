<script lang="ts">
  import { onMount, type Component } from 'svelte';
  import Empty from './components/Empty.svelte';
  import Icon from './components/Icon.svelte';
  import Shell from './components/Shell.svelte';
  import Toasts from './components/Toasts.svelte';
  import { router, safeNext } from './lib/router.svelte';
  import { session } from './lib/stores/session.svelte';
  import Approvals from './screens/Approvals.svelte';
  import Audit from './screens/Audit.svelte';
  import Chat from './screens/chat/Chat.svelte';
  import Login from './screens/Login.svelte';
  import Memory from './screens/Memory.svelte';
  import RunTrace from './screens/RunTrace.svelte';
  import ScheduleEdit from './screens/ScheduleEdit.svelte';
  import Schedules from './screens/Schedules.svelte';
  import Settings from './screens/settings/Settings.svelte';
  import Setup from './screens/setup/Setup.svelte';
  import Usage from './screens/Usage.svelte';

  // Screens take different props (all accept params), so the table is loosely typed.
  type Screen = Component<any>;

  // One bundle: it compresses far better than a chunk per screen (shared
  // Svelte and Tailwind vocabulary), and the JS budget counts every chunk.
  const screens: Record<string, Screen> = {
    '/setup': Setup,
    '/login': Login,
    '/chat': Chat,
    '/chat/:id': Chat,
    '/approvals': Approvals,
    '/approvals/:id': Approvals,
    '/runs/:id': RunTrace,
    '/schedules': Schedules,
    '/schedules/:id': ScheduleEdit,
    '/memory': Memory,
    '/usage': Usage,
    '/audit': Audit,
    '/settings/:page': Settings,
  };
  const Current: Screen | null = $derived(screens[router.current?.pattern ?? ''] ?? null);

  const pattern = $derived(router.current?.pattern ?? '');
  const isPublic = $derived(pattern === '/setup' || pattern === '/login');

  onMount(() => void session.init());

  // Route guards: setup first, then login, then the app. Once the owner
  // exists, the rest of the wizard needs their session, so a signed-out
  // browser logs in first.
  $effect(() => {
    if (!session.ready) return;
    const setup = session.setup;
    const here = router.path + router.search;
    const ownerLogin = !!setup?.needs_setup && setup.owner_set && !session.me;
    if (ownerLogin) {
      if (pattern !== '/login') router.navigate(`/login?next=${encodeURIComponent('/setup')}`, { replace: true });
    } else if (router.path === '/') {
      router.navigate(setup?.needs_setup ? '/setup' : session.me ? '/chat' : '/login', { replace: true });
    } else if (setup?.needs_setup) {
      if (pattern !== '/setup') router.navigate('/setup', { replace: true });
    } else if (!session.me) {
      if (!isPublic || pattern === '/setup') router.navigate(`/login?next=${encodeURIComponent(here)}`, { replace: true });
    } else if (isPublic && !(pattern === '/setup' && session.finishing)) {
      router.navigate(safeNext(router.query.get('next')), { replace: true });
    }
  });
</script>

{#snippet screen()}
  {#if !router.current}
    <Empty icon="search" title="Nothing here">
      This address doesn't match any page.
      {#snippet action()}<a href="/chat" class="btn btn-primary" onclick={router.link}>Go to chat</a>{/snippet}
    </Empty>
  {:else if Current}
    <Current params={router.current.params} />
  {/if}
{/snippet}

{#if !session.ready}
  <div class="grid h-dvh place-items-center" aria-busy="true">
    <span class="grid size-11 animate-pulse place-items-center rounded-xl bg-accent text-on-accent"><Icon name="bothy" size={24} /></span>
    <span class="sr-only">Loading Bothy</span>
  </div>
{:else if isPublic || !session.me}
  {@render screen()}
{:else}
  <Shell>{@render screen()}</Shell>
{/if}

<Toasts />
