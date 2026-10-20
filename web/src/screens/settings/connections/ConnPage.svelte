<script lang="ts">
  import type { Snippet } from 'svelte';
  import { Page } from '../../../components/ui';
  import { session } from '../../../lib/stores/session.svelte';
  import PageState from '../security/PageState.svelte';

  // Every Connections page: the admin check (§4.11) and the column.
  let {
    title,
    back = { href: '/settings/connections', label: 'Connections' },
    actions,
    children,
  }: {
    title: string;
    /** null on the group page: its "← Settings" shows only where the settings sidebar is hidden (§4.3). */
    back?: { href: string; label: string } | null;
    actions?: Snippet;
    children: Snippet;
  } = $props();
</script>

{#if !session.isAdmin}
  <Page title="Connections">
    <PageState kind="forbidden" />
  </Page>
{:else if back}
  <Page {title} {back} {actions}>{@render children()}</Page>
{:else}
  <Page {title} {actions} back={{ href: '/settings', label: 'Settings' }} class="lg:[&_a[href='/settings']]:hidden">{@render children()}</Page>
{/if}
