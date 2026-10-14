<script lang="ts">
  import type { Snippet } from 'svelte';
  import { Page } from '../../components/ui';
  import { session } from '../../lib/stores/session.svelte';
  import { loadReleases } from './api';

  let { title, actions, children }: { title: string; actions?: Snippet; children: Snippet } = $props();

  let hasReleases = $state(false);
  $effect(() => {
    void loadReleases().then((r) => (hasReleases = r.environments.length > 0));
  });

  const tabs = $derived([
    { label: 'Tasks', href: '/activity' },
    { label: 'Schedules', href: '/activity/schedules' },
    ...(hasReleases ? [{ label: 'Releases', href: '/activity/releases' }] : []),
    { label: 'Spending', href: '/activity/spending' },
    ...(session.isAdmin ? [{ label: 'Audit log', href: '/activity/audit' }] : []),
  ]);
</script>

<Page {title} {actions} {tabs}>{@render children()}</Page>
