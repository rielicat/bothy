<script lang="ts">
  import type { Snippet } from 'svelte';
  import { router } from '../../lib/router.svelte';
  import Avatar from './Avatar.svelte';

  let {
    href,
    title,
    description,
    meta = [],
    trailing,
    avatar,
    selected = false,
    onclick,
    class: cls = '',
  }: {
    href?: string;
    title: string | Snippet;
    description?: string;
    /** Right-aligned on wide screens; one muted line under the title on phones. */
    meta?: (string | Snippet)[];
    /** A control at the end of the row; it stays its own target, outside the row link. */
    trailing?: Snippet;
    /** A person's name, shown as initials. */
    avatar?: string;
    selected?: boolean;
    onclick?: (e: MouseEvent) => void;
    class?: string;
  } = $props();

  const twoLine = $derived(!!description);
  const titleText = $derived(typeof title === 'string' ? title : undefined);
  // A link stretches over the whole row, so the row hovers edge to edge (in a group card) and its trailing control sits above the link.
  const inner = $derived(
    `flex min-w-0 flex-1 items-center gap-3 py-2 ${twoLine ? 'min-h-14' : 'min-h-10'} pointer-coarse:min-h-12 ${href ? 'after:absolute after:inset-0 focus-visible:outline-none' : ''}`,
  );

  function click(e: MouseEvent) {
    if (href?.startsWith('/')) router.link(e);
    onclick?.(e);
  }
</script>

{#snippet piece(m: string | Snippet)}{#if typeof m === 'string'}{m}{:else}{@render m()}{/if}{/snippet}

{#snippet content()}
  {#if avatar}<Avatar name={avatar} size={twoLine ? 32 : 24} />{/if}
  <span class="min-w-0 flex-1">
    <span class="block truncate text-body text-text {href ? 'font-medium' : ''}" title={titleText}>
      {#if typeof title === 'string'}{title}{:else}{@render title()}{/if}
    </span>
    {#if description}<span class="block truncate text-small text-muted">{description}</span>{/if}
    {#if meta.length}
      <span class="block truncate text-small text-muted tabular-nums sm:hidden">
        {#each meta as m, i (i)}{#if i > 0}{' · '}{/if}{@render piece(m)}{/each}
      </span>
    {/if}
  </span>
  {#if meta.length}
    <span class="flex shrink-0 items-center gap-3 text-small text-muted tabular-nums max-sm:hidden">
      {#each meta as m, i (i)}<span>{@render piece(m)}</span>{/each}
    </span>
  {/if}
{/snippet}

<div class="flex items-center gap-3 border-b border-border last:border-b-0 {href ? 'row-stretch' : ''} {selected ? 'bg-selected hover:bg-selected' : ''} {cls}">
  {#if href}
    <a {href} class={inner} aria-current={selected ? 'true' : undefined} onclick={click}>{@render content()}</a>
  {:else}
    <div class={inner}>{@render content()}</div>
  {/if}
  {#if trailing}<div class="relative z-10 flex shrink-0 items-center">{@render trailing()}</div>{/if}
</div>
