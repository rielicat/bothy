<script lang="ts" module>
  // A danger banner is an alert the first time its text appears in this page
  // load only; when it shows again after navigation it is a plain status.
  const alerted = new Set<string>();
</script>

<script lang="ts">
  import { untrack } from 'svelte';
  import Icon from '../Icon.svelte';
  import IconButton from './IconButton.svelte';
  import LinkButton from './LinkButton.svelte';

  let {
    tone = 'neutral',
    text,
    action,
    dismissible = false,
    ondismiss,
  }: {
    tone?: 'warning' | 'danger' | 'neutral';
    text: string;
    action?: { label: string; onclick: () => void };
    /** Only when the condition doesn't stop work (§4.9). */
    dismissible?: boolean;
    ondismiss?: () => void;
  } = $props();

  const alertRole = untrack(() => tone === 'danger' && !alerted.has(text));
  if (alertRole) alerted.add(untrack(() => text));

  const bg = { warning: 'bg-warning-subtle', danger: 'bg-danger-subtle', neutral: 'bg-subtle' };
</script>

<div role={alertRole ? 'alert' : 'status'} class="flex min-h-10 items-center gap-3 px-6 py-2 text-body text-text max-sm:items-start max-sm:px-4 {bg[tone]}">
  {#if tone === 'warning'}
    <Icon name="alert" class="text-warning-text max-sm:mt-0.5" />
  {:else if tone === 'danger'}
    <Icon name="alert" class="text-danger-text max-sm:mt-0.5" />
  {/if}
  <p class="min-w-0 flex-1">
    {text}{#if action}{' '}<LinkButton class="ml-1 font-medium" onclick={action.onclick}>{action.label}</LinkButton>{/if}
  </p>
  {#if dismissible}
    <IconButton icon="close" label="Dismiss" size="sm" variant="quiet" class="-my-1 -mr-2" onclick={() => ondismiss?.()} />
  {/if}
</div>
