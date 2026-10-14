<script lang="ts">
  import ProviderChooser from './ProviderChooser.svelte';

  // A stand-in for the v0.1 provider tiles until C7's Models page imports
  // ProviderChooser itself; delete it then (docs/design/ui.md §18.3 C2).
  let { onadded }: { onadded?: (ref: string, probe: null) => void } = $props();
</script>

<ProviderChooser mode="settings" onconnected={(c) => onadded?.(`${c.id}/${c.main}`, null)} />
