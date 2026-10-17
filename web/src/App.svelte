<script lang="ts">
  import { onMount } from 'svelte';
  import NotFound from './components/shell/NotFound.svelte';
  import ScreenLoading from './components/shell/ScreenLoading.svelte';
  import { adminOnlyTitle, unknownGroup } from './components/shell/settings';
  import Shell from './components/shell/Shell.svelte';
  import LiveRegion from './components/ui/LiveRegion.svelte';
  import ToastHost from './components/ui/ToastHost.svelte';
  import { groupOf, load, prefetchIdle, prefetchLinks, type Group, type RouteGroup, type Screen } from './lazy';
  import { redirect, settingsHome, type Match, type Params } from './lib/routes';
  import { router, safeNext } from './lib/router.svelte';
  import { session } from './lib/stores/session.svelte';
  import Chat from './screens/chat/Chat.svelte';

  let Gallery: Screen | null = $state(null);

  // Groups whose chunk has arrived; the module objects never change, hence raw.
  let groups: Partial<Record<Group, RouteGroup>> = $state.raw({});
  let failed: Group | null = $state(null);

  function screenFor(m: Match | null, routes: RouteGroup | undefined): { c: Screen; params: Params } | null {
    if (!m) return null;
    const c = m.pattern === '/chat' || m.pattern === '/chat/:id' ? Chat : m.pattern === '/dev/ui' ? Gallery : (routes?.screen(m) ?? null);
    return c ? { c, params: m.params } : null;
  }

  const pattern = $derived(router.current?.pattern ?? '');
  const moved = $derived(redirect(router.path));
  const group = $derived(groupOf(pattern));
  const routes = $derived(group ? groups[group] : undefined);
  const loading = $derived(!!group && !routes && !unknownGroup(router.path));
  const current = $derived(unknownGroup(router.path) ? null : screenFor(router.current, routes));
  const isPublic = $derived(pattern === '/setup' || pattern === '/login');
  const bare = $derived(isPublic || pattern === '/password');
  const forAdmins = $derived(session.isAdmin ? null : adminOnlyTitle(router.path));

  // Wide screens have Settings in the sidebar, so /settings opens the first
  // group there; phones and tablets get the list of groups (§4.3, §4.5).
  const wide = () => window.matchMedia?.('(min-width: 1024px)').matches ?? true;
  const settingsMoves = $derived(pattern === '/settings' && wide());

  // "Loading…" only after 400 ms (§13.2), so a fast start shows nothing at all.
  let slow = $state(false);
  onMount(() => {
    void session.init();
    const t = setTimeout(() => (slow = true), 400);
    const off = prefetchLinks();
    return () => {
      clearTimeout(t);
      off();
    };
  });

  // Fetched alongside the session check, so a deep link doesn't wait twice.
  $effect(() => {
    const g = group;
    if (!g || groups[g]) return;
    failed = null;
    load(g).then(
      (r) => (groups = { ...groups, [g]: r }),
      () => (failed = g),
    );
  });

  const signedIn = $derived(!!session.me);
  $effect(() => {
    if (signedIn) return prefetchIdle(['approvals', 'activity', 'knowledge', 'settings']);
  });

  // The gallery stays out of production builds: DEV is a build-time constant,
  // so the import below is removed with the dead branch.
  $effect(() => {
    if (import.meta.env.DEV && pattern === '/dev/ui' && !Gallery) void import('./components/ui/Gallery.svelte').then((m) => (Gallery = m.default));
  });

  // Old addresses move before any guard runs, so ?next= carries the new one.
  $effect.pre(() => {
    if (moved) router.navigate(moved + router.search + router.hash, { replace: true });
  });

  // Route guards: setup first, then login, then the app. Once the owner
  // exists, the rest of the wizard needs their session, so a signed-out
  // browser logs in first.
  $effect(() => {
    if (!session.ready || moved) return;
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
    } else if (session.me.user.must_change_password) {
      // The server refuses everything else until the one-time password is replaced (S9).
      if (pattern !== '/password') router.navigate(`/password?next=${encodeURIComponent(isPublic ? safeNext(router.query.get('next')) : here)}`, { replace: true });
    } else if (isPublic && !(pattern === '/setup' && session.finishing)) {
      router.navigate(safeNext(router.query.get('next')), { replace: true });
    } else if (settingsMoves) {
      router.navigate(settingsHome(session.isAdmin), { replace: true });
    }
  });
</script>

{#snippet screen()}
  {#if moved || settingsMoves || router.path === '/' || (pattern === '/dev/ui' && !Gallery)}
    <!-- on its way to another address: / always moves on to setup, sign-in or chat -->
  {:else if forAdmins}
    <NotFound admin={forAdmins} />
  {:else if loading}
    <ScreenLoading failed={failed === group} />
  {:else if current}
    <current.c params={current.params} />
  {:else}
    <NotFound />
  {/if}
{/snippet}

{#if !session.ready}
  <div class="grid h-dvh place-items-center" aria-busy="true">
    <p class="text-body text-muted" class:invisible={!slow}>Loading…</p>
  </div>
{:else if bare || !session.me}
  {@render screen()}
{:else}
  <Shell>{@render screen()}</Shell>
{/if}

<ToastHost inset={bare || !session.me ? 'page' : 'main'} />
<LiveRegion />
