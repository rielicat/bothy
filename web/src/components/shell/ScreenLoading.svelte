<script lang="ts">
  import Loading from '../ui/Loading.svelte';

  // A screen whose chunk is still on its way. Try again reloads, which also picks up a newer
  // build whose chunks replaced the ones this page asked for.
  let { failed = false }: { failed?: boolean } = $props();
</script>

<div class="page"><Loading {failed} retry={() => window.location.reload()} /></div>
