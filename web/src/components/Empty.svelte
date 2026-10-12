<script lang="ts">
  import type { Snippet } from 'svelte';
  import type { IconName } from '../lib/icons';
  import Icon from './Icon.svelte';

  let { icon, title, children, action }: { icon: IconName; title: string; children?: Snippet; action?: Snippet } = $props();
</script>

<div class="flex flex-col items-center px-6 py-14 text-center">
  <div class="mb-4 grid size-14 place-items-center rounded-2xl bg-accent-soft text-accent">
    <Icon name={icon} size={26} />
  </div>
  <h2 class="text-base font-semibold">{title}</h2>
  {#if children}<div class="mt-1.5 max-w-sm text-sm text-muted">{@render children()}</div>{/if}
  {#if action}<div class="mt-5 flex flex-wrap justify-center gap-2">{@render action()}</div>{/if}
</div>
