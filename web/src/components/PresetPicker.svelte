<script lang="ts">
  import { config } from '../lib/config.svelte';

  let { examples = false }: { examples?: boolean } = $props();

  const presets = [
    { v: 'careful', title: 'Careful', text: 'Asks before anything leaves this box.', eg: 'Sending an email or fetching a new site always asks.' },
    { v: 'balanced', title: 'Balanced', text: 'Asks the first time per recipient or site.', eg: 'Writing to Jo again needs no new approval.' },
    { v: 'hands_off', title: 'Hands-off', text: 'Asks only for irreversible actions.', eg: 'Deleting files or paying still asks.' },
  ];
  const preset = $derived(config.value<string>('policy.preset', 'careful'));
</script>

<div class="grid gap-3 sm:grid-cols-3" role="radiogroup" aria-label="Policy preset">
  {#each presets as p (p.v)}
    <button
      type="button"
      role="radio"
      aria-checked={preset === p.v}
      disabled={config.entry('policy.preset')?.locked}
      class="card p-4 text-left hover:border-accent {preset === p.v ? 'border-accent bg-accent-soft/50 ring-1 ring-accent' : ''}"
      onclick={() => config.set({ 'policy.preset': p.v }, true)}
    >
      <p class="font-medium">{p.title}</p>
      <p class="mt-1 text-sm {examples ? '' : 'text-muted'}">{p.text}</p>
      {#if examples}<p class="mt-2 text-xs text-muted">{p.eg}</p>{/if}
    </button>
  {/each}
</div>
