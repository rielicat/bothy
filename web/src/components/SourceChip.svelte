<script lang="ts">
  import type { Entry } from '../lib/api/types.gen';
  import { config, show, sourceLabel } from '../lib/config.svelte';
  import { session } from '../lib/stores/session.svelte';
  import Icon from './Icon.svelte';

  let { entry }: { entry?: Entry } = $props();

  const me = $derived(session.me?.user.id);
  const below = $derived(entry?.shadowed?.[0]);
  const title = $derived(
    entry?.shadowed?.length ? 'Lower layers: ' + entry.shadowed.map((s) => `${sourceLabel(s.source, me)} = ${show(s.value)}`).join('; ') : '',
  );
</script>

{#if entry}
  {#if entry.locked}
    <span class="chip" title="Set in bothy.yaml or the environment; the web UI cannot change it"><Icon name="lock" size={11} />locked</span>
  {:else}
    <span class="chip {entry.source.layer === 'ui' ? 'chip-accent' : ''}" {title}>{sourceLabel(entry.source, me)}</span>
  {/if}
  {#if entry.source.layer === 'ui' && below && below.source.layer !== 'default'}
    <span class="chip chip-warn" title="The web UI value wins over {below.source.layer}">overrides {below.source.layer} value {show(below.value)}</span>
  {/if}
  {#if entry.source.layer === 'ui' && !entry.locked}
    <button type="button" class="text-xs text-muted underline-offset-2 hover:text-fg hover:underline" title="Falls back to {below ? `${show(below.value)} (${sourceLabel(below.source, me)})` : 'the default'}" onclick={() => config.reset(entry.path)}>
      Reset
    </button>
  {/if}
{/if}
