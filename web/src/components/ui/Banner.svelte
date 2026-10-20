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

<!-- The tint spans the main area; the words keep to the content column, so they start at the page's left edge (§4.9). -->
<div role={alertRole ? 'alert' : 'status'} class="text-body text-text {bg[tone]}">
  <div class="mx-auto flex min-h-10 w-full max-w-3xl items-center gap-3 px-6 py-2 max-sm:items-start max-sm:px-4">
    {#if tone === 'warning'}
      <Icon name="alert" class="shrink-0 text-warning-text max-sm:mt-0.5" />
    {:else if tone === 'danger'}
      <Icon name="alert" class="shrink-0 text-danger-text max-sm:mt-0.5" />
    {/if}
    <p class="min-w-0 flex-1">
      {text}{#if action}{' '}<LinkButton class="ml-1 font-medium" onclick={action.onclick}>{action.label}</LinkButton>{/if}
    </p>
    {#if dismissible}
      <IconButton icon="close" label="Dismiss" size="sm" variant="quiet" class="-my-1 -mr-2" onclick={() => ondismiss?.()} />
    {/if}
  </div>
</div>
