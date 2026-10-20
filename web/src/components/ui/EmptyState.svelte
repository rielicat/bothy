<script lang="ts">
  import Button, { type ButtonVariant } from './Button.svelte';

  // A sentence, an optional muted sentence and at most one action; no icon, no illustration (§13.1).
  let {
    text,
    more,
    picture = false,
    action,
    class: cls = '',
  }: {
    text: string;
    more?: string;
    /** Bothy's mark beside the text: only Approvals → Waiting (§13.1). */
    picture?: boolean;
    action?: { label: string; href?: string; onclick?: () => void; variant?: ButtonVariant };
    class?: string;
  } = $props();
</script>

<div class="{picture ? 'flex items-start gap-3' : ''} {cls}">
  {#if picture}<img src="/icon.svg" alt="" width="32" height="32" class="size-8 shrink-0" />{/if}
  <div>
  <p class="text-reading text-text">{text}</p>
  {#if more}<p class="text-reading text-muted">{more}</p>{/if}
  {#if action}
    <Button class="mt-4" variant={action.variant ?? 'primary'} href={action.href} onclick={action.onclick}>{action.label}</Button>
  {/if}
  </div>
</div>
