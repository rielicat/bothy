<script lang="ts">
  import type { Snippet } from 'svelte';
  import { Page } from '../../components/ui';
  import { session } from '../../lib/stores/session.svelte';
  import { loadReleases } from './api';

  // The heading is always "Activity", for members too: the selected tab names the part. `tab` names
  // it in the browser tab's title. Each tab keeps its own actions.
  let { tab, actions, children }: { tab?: string; actions?: Snippet; children: Snippet } = $props();

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

<!--
  A zero-width strut gives every Activity header a button's height and baseline, so the tabs
  don't jump between pages with and without actions. Tabs keep their width: the 44 px touch
  minimum replaces min-width:auto, which let labels overlap at 360 px. Both belong in
  PageHeader and Tabs (F2).
-->
{#snippet header()}
  <span class="btn invisible w-0 px-0" aria-hidden="true">&#8203;</span>
  {@render actions?.()}
{/snippet}

<Page title="Activity" docTitle={tab ? `${tab} · Activity` : undefined} actions={header} {tabs} class="[&_.tab]:shrink-0">{@render children()}</Page>
