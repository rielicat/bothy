<script lang="ts">
  import type { Component } from 'svelte';
  import { EmptyState, Page } from '../../components/ui';
  import { session } from '../../lib/stores/session.svelte';
  import Account from './Account.svelte';
  import General from './General.svelte';
  import Models from './Models.svelte';
  import People from './People.svelte';
  import Personality from './Personality.svelte';
  import { settingsConfig } from './store.svelte';

  let { params }: { params: Record<string, string> } = $props();

  type Screen = Component<any>;

  // Groups other chunks own render through here until the router takes them
  // (docs/design/ui.md §18.1 migration rule): their v0.2 screen when it
  // exists, else the v0.1 page. Globs instead of imports keep this file
  // building while those chunks add their screens and delete the old pages.
  const v1: Partial<Record<string, Screen>> = import.meta.glob<Screen>('./{Channels,Mcp,Policy,Sandbox,Vault,Advanced,Skills}.svelte', { eager: true, import: 'default' });
  const v2: Partial<Record<string, Screen>> = import.meta.glob<Screen>(
    ['./connections/{Connections,ChatApp,McpServer}.svelte', './security/{Security,Permissions,Websites,SavedKeys}.svelte', './advanced/Advanced.svelte'],
    { eager: true, import: 'default' },
  );
  const v1Config = import.meta.glob<{ config: { load(): Promise<void> } }>('../../lib/config.svelte.ts', { eager: true });

  interface Target {
    title: string;
    admin: boolean;
    c?: Screen;
    /** A v0.1 page, which expects the old frame around it. */
    legacy?: boolean;
  }

  const mine: Record<string, { title: string; c: Screen; admin: boolean }> = {
    account: { title: 'Account', c: Account, admin: false },
    general: { title: 'General', c: General, admin: true },
    models: { title: 'Models', c: Models, admin: true },
    people: { title: 'People', c: People, admin: true },
  };

  function pick(next: string, old: string, title: string): Target {
    const c = v2[`./${next}.svelte`];
    if (c) return { title, admin: true, c };
    return { title, admin: true, c: v1[`./${old}.svelte`], legacy: true };
  }

  function target(group: string | undefined, item: string | undefined, page: string | undefined): Target | null {
    if (page === 'skills') return { title: 'Skills', admin: false, c: v1['./Skills.svelte'], legacy: true };
    if (!group) return null;
    if (!item) {
      if (mine[group]) return mine[group];
      if (group === 'connections') return pick('connections/Connections', 'Channels', 'Connections');
      if (group === 'security') return pick('security/Security', 'Policy', 'Security');
      if (group === 'advanced') return pick('advanced/Advanced', 'Advanced', 'Advanced');
      return null;
    }
    if (group === 'general' && item === 'personality') return { title: 'Personality', c: Personality, admin: true };
    if (group === 'connections') {
      if (['slack', 'telegram', 'discord'].includes(item)) return pick('connections/ChatApp', 'Channels', 'Chat apps');
      if (item.startsWith('mcp-')) return pick('connections/McpServer', 'Mcp', 'Tools');
    }
    if (group === 'security') {
      const items: Record<string, [string, string, string]> = {
        permissions: ['security/Permissions', 'Policy', 'Allowed without asking'],
        websites: ['security/Websites', 'Sandbox', 'Websites Bothy can reach'],
        keys: ['security/SavedKeys', 'Vault', 'Saved keys'],
      };
      if (items[item]) return pick(...items[item]);
    }
    return null;
  }

  const t = $derived(target(params.group, params.item, params.page));
  const allowed = $derived(!!t && (!t.admin || session.isAdmin));

  // A fresh read on every settings page: nothing listened for changes while the person was elsewhere.
  $effect(() => {
    if (!session.isAdmin) return;
    void settingsConfig.load(true);
    const off = settingsConfig.listen();
    if (t?.legacy) for (const m of Object.values(v1Config)) void m.config.load();
    return off;
  });
</script>

{#if !t?.c}
  <Page title="Page not found">
    <EmptyState text="This page doesn't exist." action={{ label: 'Go to chat', href: '/chat' }} />
  </Page>
{:else if !allowed}
  <Page title={t.title}>
    <EmptyState text="Only admins can see this page." action={{ label: 'Go to chat', href: '/chat' }} />
  </Page>
{:else if t.legacy}
  <Page title={t.title}>
    <t.c {params} />
  </Page>
{:else}
  <t.c {params} />
{/if}
