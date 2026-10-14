<script lang="ts">
  import type { HTMLButtonAttributes } from 'svelte/elements';
  import type { IconName } from '../../lib/icons';
  import Icon from '../Icon.svelte';
  import type { ButtonVariant } from './Button.svelte';
  import { tooltip } from './tooltip';

  type Props = Omit<HTMLButtonAttributes, 'type' | 'disabled' | 'children' | 'class' | 'aria-label' | 'aria-pressed'> & {
    icon: IconName;
    /** The accessible name and the tooltip; required because there is no visible text. */
    label: string;
    variant?: ButtonVariant;
    size?: 'md' | 'sm';
    /** Makes it a toggle (aria-pressed). */
    pressed?: boolean;
    disabled?: boolean;
    type?: 'button' | 'submit';
    class?: string;
  };

  let { icon, label, variant = 'ghost', size = 'md', pressed, disabled = false, type = 'button', class: cls = '', onclick, ...rest }: Props = $props();
</script>

<button
  {type}
  class="btn btn-icon btn-{variant} {size === 'sm' ? 'btn-sm' : ''} {pressed ? 'bg-selected text-text' : ''} {cls}"
  aria-label={label}
  aria-pressed={pressed === undefined ? undefined : pressed}
  aria-disabled={disabled || undefined}
  onclick={(e) => {
    if (disabled) {
      e.preventDefault();
      return;
    }
    onclick?.(e);
  }}
  {@attach tooltip(() => label)}
  {...rest}><Icon name={icon} size={size === 'sm' ? 16 : 20} /></button
>
