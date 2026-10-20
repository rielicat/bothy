<script lang="ts">
  import { router } from '../../lib/router.svelte';
  import { session } from '../../lib/stores/session.svelte';
  import { shell } from '../../lib/stores/ui.svelte';
  import Icon from '../Icon.svelte';
  import Dot from '../ui/Dot.svelte';
  import IconButton from '../ui/IconButton.svelte';
  import { attention } from './attention.svelte';
  import { settingsSections } from './settings';

  // Opening Settings swaps the sidebar's contents (§4.3): same width, same colour.
  let { drawer = false }: { drawer?: boolean } = $props();
  const uid = $props.id();
  const here = (id: string) => router.path === `/settings/${id}` || router.path.startsWith(`/settings/${id}/`);
  const sections = $derived(settingsSections(session.isAdmin));
  const item =
    'group/i flex h-9 items-center gap-2.5 rounded-md px-2.5 text-body font-medium text-text hover:bg-hover focus-visible:-outline-offset-2 aria-[current=page]:bg-raised aria-[current=page]:shadow-raised pointer-coarse:h-11';
</script>

<div class="flex h-11 items-center gap-2">
  <a href={shell.back} onclick={router.link} class="{item} flex-1"><Icon name="back" size={18} class="-ml-0.5 text-muted" />Back to Bothy</a>
  {#if drawer}<IconButton icon="close" label="Close menu" variant="quiet" onclick={() => (shell.drawer = false)} />{/if}
</div>
{#each sections as s (s.key)}
  <p id="{uid}-{s.key}" class="mt-4 mb-1 px-2 text-caption text-muted">{s.label}</p>
  <ul aria-labelledby="{uid}-{s.key}">
    {#each s.groups as g (g.id)}
      <li>
        <a href="/settings/{g.id}" onclick={router.link} aria-current={here(g.id) ? 'page' : undefined} class={item}>
          <Icon name={g.icon} size={18} class="-ml-0.5 text-muted group-aria-[current=page]/i:text-accent-text" /><span class="flex-1">{g.label}</span>
          {#if g.dot && attention[g.dot]}<Dot />{/if}
        </a>
      </li>
    {/each}
  </ul>
{/each}
