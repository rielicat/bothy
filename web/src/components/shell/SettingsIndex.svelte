<script lang="ts">
  import { router } from '../../lib/router.svelte';
  import { session } from '../../lib/stores/session.svelte';
  import Icon from '../Icon.svelte';
  import Dot from '../ui/Dot.svelte';
  import Page from '../ui/Page.svelte';
  import { attention } from './attention.svelte';
  import { settingsSections } from './settings';

  // /settings on phones and tablets (docs/design/ui.md §4.3): the groups as a list.
  const uid = $props.id();
  const sections = $derived(settingsSections(session.isAdmin));
</script>

<Page title="Settings">
  {#each sections as s, i (s.key)}
    <h2 id="{uid}-{s.key}" class="mb-1 text-caption text-muted {i ? 'mt-8' : ''}">{s.label}</h2>
    <ul aria-labelledby="{uid}-{s.key}">
      {#each s.groups as g (g.id)}
        <li class="border-b border-border last:border-b-0">
          <a href="/settings/{g.id}" onclick={router.link} class="row-link flex min-h-12 items-center gap-3 text-body font-medium text-text">
            <Icon name={g.icon} size={18} class="text-muted" /><span class="flex-1">{g.label}</span>
            {#if g.dot && attention[g.dot]}<Dot />{/if}
            <Icon name="chevron-right" class="text-muted" />
          </a>
        </li>
      {/each}
    </ul>
  {/each}
</Page>
