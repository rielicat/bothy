<script lang="ts">
  import type { HTMLInputAttributes } from 'svelte/elements';

  type Props = Omit<HTMLInputAttributes, 'type' | 'checked' | 'class'> & {
    checked?: boolean;
    label: string;
    description?: string;
    class?: string;
  };

  let { checked = $bindable(false), label, description, disabled = false, class: cls = '', ...rest }: Props = $props();
  const uid = $props.id();
</script>

<!-- The label is the hit area: 32 px tall, 44 px on touch. -->
<label class="flex min-h-8 cursor-pointer items-start gap-2 py-1.5 pointer-coarse:min-h-11 pointer-coarse:py-3 {disabled ? 'cursor-not-allowed' : ''} {cls}">
  <input
    type="checkbox"
    bind:checked
    {disabled}
    class="mt-0.5 size-4 shrink-0 cursor-[inherit] accent-accent"
    aria-labelledby={description ? `${uid}-label` : undefined}
    aria-describedby={description ? `${uid}-desc` : undefined}
    {...rest}
  />
  <span class="min-w-0 text-body">
    <span id="{uid}-label" class={disabled ? 'text-faint' : 'text-text'}>{label}</span>
    {#if description}<span id="{uid}-desc" class="block text-small text-muted">{description}</span>{/if}
  </span>
</label>
