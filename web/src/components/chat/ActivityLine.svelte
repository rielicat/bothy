<script module lang="ts">
  let lastSaid = 0;
</script>

<script lang="ts">
  import { untrack } from 'svelte';
  import { lineFor, plain } from '../../lib/activity';
  import Icon from '../Icon.svelte';
  import { announce } from '../ui/live.svelte';
  import Spinner from '../ui/Spinner.svelte';
  import PhraseText from './PhraseText.svelte';
  import StepDetails from './StepDetails.svelte';
  import type { StepView } from './steps';

  // `card`: the pending card follows right below, and whether the viewer decides it.
  let { step, card }: { step: StepView; card?: 'yours' | 'theirs' } = $props();

  let open = $state(false);
  const line = $derived(lineFor(step.phrases, step.state, card));
  const tone = $derived(step.state === 'waiting' ? 'text-warning-text' : 'text-muted');

  // Only a step turning to waiting or failed is read out, at most once a second (§16).
  let seen = untrack(() => step.state);
  $effect(() => {
    const s = step.state;
    if (s === seen) return;
    seen = s;
    if ((s === 'waiting' || s === 'failed') && Date.now() - lastSaid > 1000) {
      lastSaid = Date.now();
      announce(plain(untrack(() => line)));
    }
  });
</script>

<div>
  <div class="flex items-start">
    <button
      type="button"
      class="flex min-h-8 min-w-0 flex-1 cursor-pointer items-start gap-2 rounded-md py-1.5 text-left text-body hover:text-text pointer-coarse:min-h-11 pointer-coarse:py-3 {tone}"
      aria-expanded={open}
      onclick={() => (open = !open)}
    >
      <span class="flex h-5 w-4 shrink-0 items-center justify-center">
        {#if step.state === 'running'}
          <Spinner size={16} />
        {:else if step.state === 'waiting'}
          <Icon name="clock" />
        {:else if step.state === 'failed'}
          <Icon name="alert" class="text-danger-text" />
        {:else if step.state !== 'done'}
          <Icon name="close" />
        {/if}
      </span>
      <span class="min-w-0 [overflow-wrap:anywhere]"><PhraseText phrase={line} /></span>
    </button>
    {#if step.link}
      <a
        class="link inline-flex min-h-8 min-w-6 shrink-0 items-center justify-center text-body pointer-coarse:min-h-11 pointer-coarse:min-w-11"
        href={step.link}
        target="_blank"
        rel="noopener noreferrer">↗<span class="sr-only">Open on GitHub (opens in a new tab)</span></a
      >
    {/if}
  </div>
  {#if step.why}<p class="pb-1 pl-6 text-small text-muted">{step.why}</p>{/if}
  {#if open}<StepDetails {step} />{/if}
</div>
