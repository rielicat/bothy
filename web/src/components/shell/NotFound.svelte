<script lang="ts">
  import EmptyState from '../ui/EmptyState.svelte';
  import Page from '../ui/Page.svelte';

  // docs/design/ui.md §13.1: an address that doesn't exist, or an admin page a member opened.
  let { admin }: { admin?: string } = $props();
</script>

<Page title={admin ?? 'Page not found'}>
  <EmptyState text={admin ? 'Only admins can see this page.' : "Check the address, or go back to your chats."} action={{ label: 'Go to chat', href: '/chat' }} />
</Page>
