<script lang="ts">
  import type { Snippet } from 'svelte';
  import Icon from '../Icon.svelte';

  // Help or error under a field: an error replaces the help (§15.3 row 4).
  let { id, help, error }: { id: string; help?: string | Snippet; error?: string } = $props();
</script>

{#if error}
  <p id="{id}-error" class="field-error"><Icon name="alert" class="mt-px" /><span class="min-w-0">{error}</span></p>
{:else if help}
  <p id="{id}-help" class="field-help">{#if typeof help === 'string'}{help}{:else}{@render help()}{/if}</p>
{/if}
