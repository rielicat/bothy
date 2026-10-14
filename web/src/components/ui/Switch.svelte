<script lang="ts">
  import type { HTMLButtonAttributes } from 'svelte/elements';

  type Props = Omit<HTMLButtonAttributes, 'type' | 'role' | 'disabled' | 'class' | 'onchange'> & {
    checked?: boolean;
    label: string;
    /** Keeps the label for screen readers only, when a settings row already shows it. */
    hideLabel?: boolean;
    disabled?: boolean;
    /** Why it can't be changed, shown next to it. Implies disabled. */
    reason?: string;
    onchange?: (checked: boolean) => void;
    class?: string;
  };

  let { checked = $bindable(false), label, hideLabel = false, disabled = false, reason, onchange, id: idProp, class: cls = '', ...rest }: Props = $props();

  const uid = $props.id();
  const id = $derived(idProp ?? `${uid}-switch`);
  const blocked = $derived(disabled || !!reason);

  function toggle() {
    if (blocked) return;
    checked = !checked;
    onchange?.(checked);
  }
</script>

<span class="inline-flex items-center gap-3 {cls}">
  <!-- The button is the 24 px (44 px on touch) target; the 36 × 20 track inside carries the focus ring. -->
  <button
    {id}
    type="button"
    role="switch"
    aria-checked={checked}
    aria-label={hideLabel ? label : undefined}
    aria-disabled={blocked || undefined}
    aria-describedby={reason ? `${uid}-reason` : undefined}
    class="group inline-flex h-6 w-9 shrink-0 cursor-pointer items-center justify-center focus-visible:outline-none aria-disabled:cursor-not-allowed pointer-coarse:size-11"
    onclick={toggle}
    {...rest}
  >
    <span
      class="relative h-5 w-9 rounded-full transition-colors duration-120 group-focus-visible:outline-2 group-focus-visible:outline-offset-2 group-focus-visible:outline-focus {checked
        ? blocked
          ? 'bg-control'
          : 'bg-accent'
        : blocked
          ? 'bg-border'
          : 'bg-control'}"
    >
      <span
        class="absolute top-0.5 left-0.5 size-4 rounded-full transition-transform duration-120 {checked ? 'translate-x-4' : ''} {blocked ? 'bg-surface' : 'bg-on-accent'}"
      ></span>
    </span>
  </button>
  {#if !hideLabel}<label for={id} class="cursor-pointer text-body {blocked ? 'text-faint' : 'text-text'}">{label}</label>{/if}
  {#if reason}<span id="{uid}-reason" class="text-small text-muted">{reason}</span>{/if}
</span>
