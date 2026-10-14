<script lang="ts">
  import Button, { type ButtonVariant } from './Button.svelte';

  // A sentence, an optional muted sentence and at most one action; no icon, no illustration (§13.1).
  let {
    text,
    more,
    action,
    class: cls = '',
  }: {
    text: string;
    more?: string;
    action?: { label: string; href?: string; onclick?: () => void; variant?: ButtonVariant };
    class?: string;
  } = $props();
</script>

<div class={cls}>
  <p class="text-reading text-text">{text}</p>
  {#if more}<p class="text-reading text-muted">{more}</p>{/if}
  {#if action}
    <Button class="mt-4" variant={action.variant ?? 'primary'} href={action.href} onclick={action.onclick}>{action.label}</Button>
  {/if}
</div>
