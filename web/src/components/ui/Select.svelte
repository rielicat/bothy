<script lang="ts" module>
  export interface SelectOption {
    value: string;
    label: string;
    description?: string;
  }
</script>

<script lang="ts">
  import type { Snippet } from 'svelte';
  import type { HTMLSelectAttributes } from 'svelte/elements';
  import Icon from '../Icon.svelte';
  import FieldMessage from './FieldMessage.svelte';

  type Props = Omit<HTMLSelectAttributes, 'value' | 'class'> & {
    /** Leave out inside a SettingsRow, whose label names the select through the id it passes. */
    label?: string;
    /** Keeps the label for screen readers only. */
    hideLabel?: boolean;
    value?: string;
    options: SelectOption[];
    help?: string | Snippet;
    error?: string;
    class?: string;
  };

  let { label, hideLabel = false, value = $bindable(''), options, help, error, id: idProp, class: cls = '', ...rest }: Props = $props();

  const uid = $props.id();
  const id = $derived(idProp ?? `${uid}-select`);
  const describedBy = $derived(error ? `${uid}-error` : help ? `${uid}-help` : undefined);
</script>

<div class={cls}>
  {#if label}<label for={id} class="field-label {hideLabel ? 'sr-only' : ''}">{label}</label>{/if}
  <div class="relative {label && !hideLabel ? 'mt-2' : ''}">
    <select bind:value {id} class="input cursor-pointer" aria-invalid={error ? 'true' : undefined} aria-describedby={describedBy} {...rest}>
      {#each options as o (o.value)}
        <option value={o.value} title={o.description}>{o.label}</option>
      {/each}
    </select>
    <Icon name="chevron-down" class="pointer-events-none absolute top-1/2 right-2.5 -translate-y-1/2 text-muted" />
  </div>
  <FieldMessage id={uid} {help} {error} />
</div>
