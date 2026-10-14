<script lang="ts">
  import type { Snippet } from 'svelte';
  import type { HTMLButtonAttributes } from 'svelte/elements';

  type Props = Omit<HTMLButtonAttributes, 'type' | 'children' | 'class' | 'disabled'> & {
    onclick?: (e: MouseEvent) => void;
    disabled?: boolean;
    class?: string;
    children: Snippet;
  };

  let { onclick, disabled = false, class: cls = '', children, ...rest }: Props = $props();
</script>

<!-- Takes the surrounding text size. The negative margins grow the hit area to 24 px (44 px on touch) without changing the line height. -->
<button
  type="button"
  class="-my-0.5 inline-flex min-h-6 cursor-pointer items-center text-accent-text underline decoration-accent-text/40 underline-offset-2 hover:decoration-accent-text aria-disabled:cursor-not-allowed aria-disabled:text-faint aria-disabled:no-underline pointer-coarse:-my-3 pointer-coarse:min-h-11 pointer-coarse:min-w-11 pointer-coarse:justify-center {cls}"
  aria-disabled={disabled || undefined}
  onclick={(e) => {
    if (disabled) return;
    onclick?.(e);
  }}
  {...rest}>{@render children()}</button
>
