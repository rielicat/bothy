<script lang="ts">
  import ProviderChooser from '../../components/ProviderChooser.svelte';
  import Frame from './Frame.svelte';

  // Step 2 (docs/design/ui.md §5.6). No Back: the account already exists.
  let { onnext }: { onnext: () => void } = $props();
</script>

<Frame step={2} title="Connect an AI model" intro="Bothy needs a model to think with. Choose one you already have access to.">
  <ProviderChooser onconnected={onnext} />
</Frame>
