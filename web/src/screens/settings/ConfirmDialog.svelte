<script lang="ts">
  import type { Snippet } from 'svelte';
  import { Button, Dialog, Notice } from '../../components/ui';

  // A destructive confirmation (§14.2): the consequence, then Cancel, which
  // has focus, and the red verb.
  let {
    open = $bindable(false),
    title,
    confirm,
    busyLabel,
    cancel = 'Cancel',
    busy = false,
    error = '',
    onconfirm,
    onclose,
    children,
  }: {
    open?: boolean;
    title: string;
    confirm: string;
    busyLabel?: string;
    cancel?: string;
    busy?: boolean;
    /** Shown above the buttons when the action failed. */
    error?: string;
    onconfirm: () => void;
    onclose?: () => void;
    children: Snippet;
  } = $props();
</script>

<Dialog bind:open {title} initialFocus="cancel" {busy} {onclose}>
  <p class="text-body text-text">{@render children()}</p>
  {#if error}<Notice class="mt-3" tone="danger" text={error} role="alert" />{/if}
  {#snippet footer({ close })}
    <Button data-cancel onclick={close} disabled={busy}>{cancel}</Button>
    <Button variant="danger" loading={busy} loadingLabel={busyLabel} onclick={onconfirm}>{confirm}</Button>
  {/snippet}
</Dialog>
