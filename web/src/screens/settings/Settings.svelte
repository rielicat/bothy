<script lang="ts">
  import type { Component } from 'svelte';
  import { EmptyState, Page } from '../../components/ui';
  import { session } from '../../lib/stores/session.svelte';
  import Account from './Account.svelte';
  import General from './General.svelte';
  import Models from './Models.svelte';
  import People from './People.svelte';
  import Personality from './Personality.svelte';
  import PageState from './security/PageState.svelte';
  import { settingsConfig } from './store.svelte';

  let { params }: { params: Record<string, string> } = $props();

  type Screen = Component<any>;

  interface Target {
    title: string;
    admin: boolean;
    c: Screen;
  }

  const groups: Record<string, Target> = {
    account: { title: 'Account', c: Account, admin: false },
    general: { title: 'General', c: General, admin: true },
    models: { title: 'Models', c: Models, admin: true },
    people: { title: 'People', c: People, admin: true },
  };

  function target(group: string | undefined, item: string | undefined): Target | null {
    if (!group) return null;
    if (!item) return groups[group] ?? null;
    if (group === 'general' && item === 'personality') return { title: 'Personality', c: Personality, admin: true };
    return null;
  }

  const t = $derived(target(params.group, params.item));
  const allowed = $derived(!!t && (!t.admin || session.isAdmin));

  // A fresh read on every settings page: nothing listened for changes while the person was elsewhere.
  $effect(() => {
    if (!session.isAdmin) return;
    void settingsConfig.load(true);
    return settingsConfig.listen();
  });
</script>

{#if !t?.c}
  <Page title="Page not found">
    <EmptyState text="Check the address, or go back to your chats." action={{ label: 'Go to chat', href: '/chat' }} />
  </Page>
{:else if !allowed}
  <Page title={t.title}>
    <PageState kind="forbidden" />
  </Page>
{:else}
  <t.c {params} />
{/if}
