<script lang="ts">
  import type { Snippet } from 'svelte';
  import PageHeader from '../../components/ui/PageHeader.svelte';
  import Tabs from '../../components/ui/Tabs.svelte';
  import { router } from '../../lib/router.svelte';
  import HeaderSpacer from './HeaderSpacer.svelte';
  import SearchField from './SearchField.svelte';

  // The frame of the three Knowledge tabs (§9): title, the tabs, then the tab. `search` puts the
  // code-and-notes search under the tabs: only Projects searches, and only once a repository was read
  // (⌘K Search covers the rest).
  let { title = 'Knowledge', actions, search = false, children }: { title?: string; actions?: Snippet; search?: boolean; children: Snippet } = $props();

  let q = $state('');
  const tabs = [
    { label: 'Projects', href: '/knowledge' },
    { label: 'Memory', href: '/knowledge/memory' },
    { label: 'Skills', href: '/knowledge/skills' },
  ];
</script>

<!-- With the spacer the header is one button tall on every tab, so the tabs never jump. -->
{#snippet headerActions()}<HeaderSpacer />{@render actions?.()}{/snippet}

<svelte:head><title>{title} · Bothy</title></svelte:head>

<div class="page">
  <PageHeader title="Knowledge" actions={headerActions} />
  <Tabs class="mt-4" {tabs} />
  {#if search}<SearchField class="mt-6" bind:value={q} onsearch={(t) => t && router.navigate(`/knowledge/search?q=${encodeURIComponent(t)}`)} />{/if}
  <div class="mt-6">{@render children()}</div>
</div>
