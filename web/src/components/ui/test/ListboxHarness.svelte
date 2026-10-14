<script lang="ts">
  import Listbox, { type ListboxOption } from '../Listbox.svelte';

  let { options, value = $bindable(), onchange }: { options: ListboxOption[]; value?: string; onchange?: (v: string) => void } = $props();
</script>

<Listbox bind:value {options} label="Model" {onchange}>
  {#snippet trigger(props, selected)}<button type="button" {...props}>{selected?.label ?? 'Choose a model'}</button>{/snippet}
</Listbox>
