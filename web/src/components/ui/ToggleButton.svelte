<script lang="ts">
  import type { IconName } from '../../lib/icons';
  import Icon from '../Icon.svelte';
  import { tooltip as tip } from './tooltip';

  let {
    pressed = $bindable(false),
    label,
    tooltip,
    icon,
    onchange,
    class: cls = '',
  }: { pressed?: boolean; label: string; tooltip?: string; icon?: IconName; onchange?: (pressed: boolean) => void; class?: string } = $props();
</script>

<button
  type="button"
  class="btn btn-quiet btn-sm {pressed ? 'bg-accent-subtle text-accent-text hover:bg-accent-subtle hover:text-accent-text' : ''} {cls}"
  aria-pressed={pressed}
  onclick={() => {
    pressed = !pressed;
    onchange?.(pressed);
  }}
  {@attach tip(() => tooltip, { describe: true })}
>
  {#if icon}<Icon name={icon} />{/if}{label}
</button>
