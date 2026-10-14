<script lang="ts">
  import type { Snippet } from 'svelte';
  import { EmptyState, Page } from '../../../components/ui';
  import { session } from '../../../lib/stores/session.svelte';

  // Every Connections page: the admin check (§4.11), the column, and the toast
  // host and live region, which defer to the shell's own once C1 mounts them.
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

  // A ⋯ button has no text baseline, so baseline alignment would push the title
  // down; centring it keeps the title where it sits on every other page.
  // TODO(C10): move into PageHeader.
  const iconActions = '[&_h1+div]:self-center';
</script>

{#if !session.isAdmin}
  <Page title="Connections">
    <EmptyState text="Only admins can see this page." action={{ label: 'Go to chat', href: '/chat' }} />
  </Page>
{:else if back}
  <Page {title} {back} {actions} class={iconActions}>{@render children()}</Page>
{:else}
  <Page {title} {actions} back={{ href: '/settings', label: 'Settings' }} class="{iconActions} lg:[&_a[href='/settings']]:hidden">{@render children()}</Page>
{/if}
