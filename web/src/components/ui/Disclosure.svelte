<script lang="ts">
  import { untrack, type Snippet } from 'svelte';
  import Icon from '../Icon.svelte';
  import { prefersReducedMotion } from './layer';

  let {
    label = 'Details',
    open = $bindable(false),
    variant = 'details',
    children,
    class: cls = '',
  }: {
    label?: string;
    open?: boolean;
    /** details: small muted "Details" (technical content); inline: a link-style toggle in running text; section: "More options"; step: a row among a chat's steps, on their grid. */
    variant?: 'details' | 'inline' | 'section' | 'step';
    children: Snippet;
    class?: string;
  } = $props();

  const uid = $props.id();
  // Stays true while the panel animates closed; closed panels render nothing.
  let shown = $state(untrack(() => open));
  let panel: HTMLElement | undefined = $state();

  const EASE_OUT = 'cubic-bezier(0.2, 0, 0, 1)';
  const EASE_IN = 'cubic-bezier(0.4, 0, 1, 1)';
  const frames = [
    { gridTemplateRows: '0fr', opacity: 0 },
    { gridTemplateRows: '1fr', opacity: 1 },
  ];
  const animates = (el: HTMLElement | undefined): el is HTMLElement => !!el && typeof el.animate === 'function' && !prefersReducedMotion();

  $effect(() => {
    if (open) {
      shown = true;
      return;
    }
    if (!untrack(() => shown)) return;
    const el = untrack(() => panel);
    if (!animates(el)) {
      shown = false;
      return;
    }
    el.style.overflow = 'hidden';
    const a = el.animate([...frames].reverse(), { duration: 160, easing: EASE_IN, fill: 'forwards' });
    a.onfinish = () => (shown = false);
    return () => {
      a.cancel();
      el.style.overflow = '';
    };
  });

  function reveal(el: HTMLElement) {
    if (!animates(el)) return;
    el.style.overflow = 'hidden';
    const a = el.animate(frames, { duration: 160, easing: EASE_OUT });
    // Focus rings of controls near the edge would be clipped if the overflow stayed.
    a.onfinish = () => (el.style.overflow = '');
  }

  const buttonClass = {
    details:
      '-my-0.5 inline-flex min-h-6 cursor-pointer items-center gap-1 text-small text-muted hover:text-text pointer-coarse:-my-3 pointer-coarse:min-h-11 pointer-coarse:min-w-11 pointer-coarse:justify-center',
    inline:
      '-my-0.5 inline-flex min-h-6 cursor-pointer items-center gap-1 text-accent-text underline decoration-accent-text/40 underline-offset-2 hover:decoration-accent-text pointer-coarse:-my-3 pointer-coarse:min-h-11 pointer-coarse:min-w-11 pointer-coarse:justify-center',
    section: 'inline-flex h-9 cursor-pointer items-center gap-1.5 text-body font-medium text-text pointer-coarse:h-11',
    step: 'flex min-h-8 cursor-pointer items-start gap-2 py-1.5 text-left text-body text-muted hover:text-text pointer-coarse:min-h-11 pointer-coarse:py-3',
  };
</script>

<svelte:element this={variant === 'inline' ? 'span' : 'div'} class={cls}>
  <button type="button" class={buttonClass[variant]} aria-expanded={open} aria-controls="{uid}-panel" onclick={() => (open = !open)}>
    {#if variant === 'step'}
      <span class="flex h-5 w-4 shrink-0 items-center justify-center"><Icon name="chevron-right" size={12} class="transition-transform duration-160 {open ? 'rotate-90' : ''}" /></span>
    {:else if variant !== 'inline'}<Icon name="chevron-right" size={12} class="transition-transform duration-160 {open ? 'rotate-90' : ''}" />{/if}
    {label}
    {#if variant === 'inline'}<Icon name="chevron-right" size={12} class="transition-transform duration-160 {open ? 'rotate-90' : ''}" />{/if}
  </button>
  <svelte:element
    this={variant === 'inline' ? 'span' : 'div'}
    id="{uid}-panel"
    class="block"
    data-details={variant === 'details' ? '' : undefined}
  >
    {#if shown}
      <div bind:this={panel} class="grid {variant === 'section' ? 'mt-3' : variant === 'step' ? 'mb-2 pl-6' : 'mt-2'}" {@attach reveal}>
        <div class="min-h-0">{@render children()}</div>
      </div>
    {/if}
  </svelte:element>
</svelte:element>
