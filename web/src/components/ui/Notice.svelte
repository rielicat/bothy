<script lang="ts">
  import type { Snippet } from 'svelte';
  import type { HTMLAttributes } from 'svelte/elements';
  import Icon from '../Icon.svelte';
  import LinkButton from './LinkButton.svelte';

  // There is no success notice: a success is a StatusLine at the moment it happens (§3.1).
  let {
    tone = 'neutral',
    text,
    action,
    children,
    class: cls = '',
    ...rest
  }: HTMLAttributes<HTMLDivElement> & {
    tone?: 'neutral' | 'warning' | 'danger';
    text?: string;
    action?: { label: string; onclick: () => void };
    children?: Snippet;
    class?: string;
  } = $props();

  const bg = { neutral: 'bg-subtle', warning: 'bg-warning-subtle', danger: 'bg-danger-subtle' };
</script>

<div class="flex items-start gap-2 rounded-md px-3 py-2 text-body text-text {bg[tone]} {cls}" {...rest}>
  {#if tone === 'warning'}
    <Icon name="alert" class="mt-0.5 text-warning-text" />
  {:else if tone === 'danger'}
    <Icon name="alert" class="mt-0.5 text-danger-text" />
  {/if}
  <p class="min-w-0 flex-1">
    {#if children}{@render children()}{:else}{text}{/if}
    {#if action}{' '}<LinkButton class="ml-1" onclick={action.onclick}>{action.label}</LinkButton>{/if}
  </p>
</div>
