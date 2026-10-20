<script lang="ts">
  import type { Snippet } from 'svelte';
  import type { Tone } from './modelHelpers';
  import type { IconName } from '../../lib/icons';
  import Icon from '../../components/Icon.svelte';

  // A thing with a status and one button (a provider, a linked app, a token):
  // name in a 120 px column, the status word beside it, the button at the end.
  // `stacked` puts the status under the name, for long names.
  let {
    name,
    icon,
    status,
    tone = 'muted',
    stacked = false,
    trailing,
  }: {
    name: string;
    /** A brand mark before the name (Account → Sign-in methods). */
    icon?: IconName;
    status: string;
    tone?: Tone;
    stacked?: boolean;
    trailing?: Snippet;
  } = $props();

  const color: Record<Tone, string> = { muted: 'text-muted', warning: 'text-warning-text', danger: 'text-danger-text' };
</script>

<div class="flex min-h-14 items-center gap-3 border-b border-border py-2 last:border-b-0">
  {#if icon}<Icon name={icon} />{/if}
  <div class="min-w-0 flex-1 {stacked ? '' : 'sm:flex sm:items-baseline sm:gap-4'}">
    <p class="truncate text-body font-medium text-text {stacked ? '' : 'sm:w-30 sm:shrink-0'}" title={name}>{name}</p>
    <p class="min-w-0 text-small {color[tone]} {stacked ? '' : 'sm:flex-1'}">{status}</p>
  </div>
  {#if trailing}<div class="flex shrink-0 items-center gap-2">{@render trailing()}</div>{/if}
</div>
