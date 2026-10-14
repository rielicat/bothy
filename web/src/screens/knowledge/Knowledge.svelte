<script lang="ts">
  import type { Snippet } from 'svelte';
  import PageHeader from '../../components/ui/PageHeader.svelte';
  import Tabs from '../../components/ui/Tabs.svelte';
  import { router } from '../../lib/router.svelte';
  import HeaderSpacer from './HeaderSpacer.svelte';
  import SearchField from './SearchField.svelte';

  // The frame of the three Knowledge tabs (§9): title, a search field above the tabs, then the tab.
  let { title = 'Knowledge', actions, children }: { title?: string; actions?: Snippet; children: Snippet } = $props();

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
  <SearchField class="mt-4" bind:value={q} onsearch={(t) => t && router.navigate(`/knowledge/search?q=${encodeURIComponent(t)}`)} />
  <Tabs class="mt-4" {tabs} />
  <div class="mt-6">{@render children()}</div>
</div>
