<script lang="ts">
  import { onMount, type Component } from 'svelte';
  import Shell from './components/Shell.svelte';
  import Toasts from './components/Toasts.svelte';
  import { legacySettingsPage, redirect, settingsHome, type Match, type Params } from './lib/routes';
  import { router, safeNext } from './lib/router.svelte';
  import { session } from './lib/stores/session.svelte';
  import Releases from './screens/activity/Releases.svelte';
  import Tasks from './screens/activity/Tasks.svelte';
  import Approvals from './screens/Approvals.svelte';
  import Audit from './screens/Audit.svelte';
  import ChangePassword from './screens/ChangePassword.svelte';
  import Chat from './screens/chat/Chat.svelte';
  import KnowledgeSearch from './screens/knowledge/KnowledgeSearch.svelte';
  import Memory from './screens/knowledge/Memory.svelte';
  import ProjectNotes from './screens/knowledge/ProjectNotes.svelte';
  import Projects from './screens/knowledge/Projects.svelte';
  import SkillView from './screens/knowledge/SkillView.svelte';
  import Skills from './screens/knowledge/Skills.svelte';
  import Login from './screens/Login.svelte';
  import RunTrace from './screens/RunTrace.svelte';
  import ScheduleEdit from './screens/ScheduleEdit.svelte';
  import Schedules from './screens/Schedules.svelte';
  import GitHub from './screens/settings/connections/GitHub.svelte';
  import WebSearch from './screens/settings/connections/WebSearch.svelte';
  import Settings from './screens/settings/Settings.svelte';
  import Setup from './screens/setup/Setup.svelte';
  import Usage from './screens/Usage.svelte';

  // Screens take different props (all accept params), so the table is loosely typed.
  type Screen = Component<any>;

  // One bundle: it compresses far better than a chunk per screen (shared
  // Svelte and Tailwind vocabulary), and the JS budget counts every chunk.
  // v0.2 addresses point at the v0.1 screen that covers them until the chunk
  // that owns the address replaces it (docs/design/ui.md §18.1).
  const screens: Partial<Record<string, Screen>> = {
    '/setup': Setup,
    '/login': Login,
    '/password': ChangePassword,
    '/chat': Chat,
    '/chat/:id': Chat,
    '/approvals': Approvals,
    '/approvals/:id': Approvals,
    '/knowledge': Projects,
    '/knowledge/search': KnowledgeSearch,
    '/knowledge/memory': Memory,
    '/knowledge/skills': Skills,
    '/knowledge/skills/:name': SkillView,
    '/knowledge/projects/:owner/:repo': ProjectNotes,
    '/activity': Tasks,
    '/activity/tasks/:id': RunTrace,
    '/activity/schedules': Schedules,
    '/activity/schedules/:id': ScheduleEdit,
    '/activity/releases': Releases,
    '/activity/spending': Usage,
    '/activity/audit': Audit,
  };

  let Gallery: Screen | null = $state(null);

  function screenFor(m: Match | null): { c: Screen; params: Params } | null {
    if (!m) return null;
    const p = m.params;
    switch (m.pattern) {
      case '/settings/:group':
        return { c: Settings, params: { ...p, page: legacySettingsPage(p.group) ?? p.group } };
      case '/settings/:group/:item': {
        const page = legacySettingsPage(p.group, p.item);
        if (page) return { c: Settings, params: { ...p, page } };
        if (p.group === 'connections' && p.item === 'github') return { c: GitHub, params: p };
        if (p.group === 'connections' && p.item === 'search') return { c: WebSearch, params: p };
        return null;
      }
      case '/dev/ui':
        return Gallery ? { c: Gallery, params: p } : null;
    }
    const c = screens[m.pattern];
    return c ? { c, params: p } : null;
  }

  const pattern = $derived(router.current?.pattern ?? '');
  const moved = $derived(redirect(router.path));
  const current = $derived(screenFor(router.current));
  const isPublic = $derived(pattern === '/setup' || pattern === '/login');
  const bare = $derived(isPublic || pattern === '/password');

  // "Loading…" only after 400 ms (§13.2), so a fast start shows nothing at all.
  let slow = $state(false);
  onMount(() => {
    void session.init();
    const t = setTimeout(() => (slow = true), 400);
    return () => clearTimeout(t);
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
    } else if (isPublic && !(pattern === '/setup' && session.finishing)) {
      router.navigate(safeNext(router.query.get('next')), { replace: true });
    } else if (pattern === '/settings') {
      router.navigate(settingsHome(session.isAdmin), { replace: true });
    }
  });
</script>

{#snippet screen()}
  {#if moved || pattern === '/settings' || (pattern === '/dev/ui' && !Gallery)}
    <!-- on its way to another address -->
  {:else if current}
    <current.c params={current.params} />
  {:else}
    <div class="page">
      <h1 class="page-title" tabindex="-1">Page not found</h1>
      <p class="mt-6 text-reading text-text">This page doesn't exist.</p>
      <a href="/chat" class="btn btn-primary mt-4" onclick={router.link}>Go to chat</a>
    </div>
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

<Toasts />
