<script lang="ts" module>
  export type ButtonVariant = 'primary' | 'secondary' | 'ghost' | 'quiet' | 'danger' | 'danger-text';
</script>

<script lang="ts">
  import type { Snippet } from 'svelte';
  import type { HTMLButtonAttributes } from 'svelte/elements';
  import { router } from '../../lib/router.svelte';
  import type { IconName } from '../../lib/icons';
  import Icon from '../Icon.svelte';
  import Spinner from './Spinner.svelte';

  type Props = Omit<HTMLButtonAttributes, 'type' | 'disabled' | 'onclick' | 'children' | 'class'> & {
    variant?: ButtonVariant;
    size?: 'md' | 'sm';
    icon?: IconName;
    loading?: boolean;
    /** Replaces the label while loading: "Saving…". */
    loadingLabel?: string;
    /** Renders a link styled as a button. In-app paths navigate without a reload. */
    href?: string;
    /** Opens in a new tab and ends the label with ↗ (§14.2). */
    external?: boolean;
    /** With href: the browser saves the file instead of opening it; a string names the file. */
    download?: boolean | string;
    type?: 'button' | 'submit' | 'reset';
    disabled?: boolean;
    /** Why the button can't be used yet, shown next to it and linked with aria-describedby. Implies disabled. */
    reason?: string;
    onclick?: (e: MouseEvent) => void;
    /** Called instead of onclick when a disabled button is pressed, e.g. to move focus to what's missing. */
    onblocked?: () => void;
    class?: string;
    children: Snippet;
  };

  let {
    variant = 'secondary',
    size = 'md',
    icon,
    loading = false,
    loadingLabel,
    href,
    external = false,
    download,
    type = 'button',
    disabled = false,
    reason,
    onclick,
    onblocked,
    class: cls = '',
    children,
    ...rest
  }: Props = $props();

  const uid = $props.id();
  const blocked = $derived(disabled || !!reason);
  const classes = $derived(['btn', `btn-${variant}`, size === 'sm' ? 'btn-sm' : '', cls].filter(Boolean).join(' '));
  // Brand marks carry fine detail, so they draw a size up (§3.7).
  const iconSize = $derived(icon === 'google' || icon === 'github' ? 18 : 16);

  let el: HTMLElement | undefined = $state();
  let lockedWidth: number | null = $state(null);

  // Lock the width before the label swaps, so the button doesn't jump while it works.
  $effect.pre(() => {
    if (loading) {
      if (lockedWidth === null && el) lockedWidth = el.getBoundingClientRect().width || null;
    } else lockedWidth = null;
  });

  function click(e: MouseEvent) {
    if (blocked || loading) {
      e.preventDefault();
      if (blocked) onblocked?.();
      return;
    }
    if (href && !external && !download && href.startsWith('/')) router.link(e);
    onclick?.(e);
  }
</script>

{#snippet body()}
  {#if loading}
    <Spinner />
    <span>{#if loadingLabel}{loadingLabel}{:else}{@render children()}{/if}</span>
  {:else}
    {#if icon}<Icon name={icon} size={iconSize} />{/if}
    <span>{@render children()}{#if external}<span aria-hidden="true">{' ↗'}</span><span class="sr-only">{' (opens in a new tab)'}</span>{/if}</span>
  {/if}
{/snippet}

{#if href}
  <a
    bind:this={el}
    href={blocked ? undefined : href}
    role={blocked ? 'link' : undefined}
    class={classes}
    aria-disabled={blocked || undefined}
    aria-describedby={reason ? `${uid}-reason` : undefined}
    aria-busy={loading || undefined}
    target={external ? '_blank' : undefined}
    rel={external ? 'noopener noreferrer' : undefined}
    download={download === true ? '' : download || undefined}
    tabindex={blocked ? 0 : undefined}
    style:min-width={lockedWidth ? `${lockedWidth}px` : undefined}
    onclick={click}
    {...rest as Record<string, unknown>}>{@render body()}</a
  >
{:else}
  <button
    bind:this={el}
    {type}
    class={classes}
    aria-disabled={blocked || undefined}
    aria-describedby={reason ? `${uid}-reason` : undefined}
    aria-busy={loading || undefined}
    style:min-width={lockedWidth ? `${lockedWidth}px` : undefined}
    onclick={click}
    {...rest}>{@render body()}</button
  >
{/if}
{#if reason}<span id="{uid}-reason" class="text-small text-muted">{reason}</span>{/if}
