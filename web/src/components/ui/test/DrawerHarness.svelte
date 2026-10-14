<script lang="ts">
  import Drawer from '../Drawer.svelte';

  let open = $state(false);
</script>

<button type="button" onclick={() => (open = true)}>Open drawer</button>
<Drawer bind:open title="Chat details">
  <a href="/chat">First link</a>
  <button type="button">Last button</button>
</Drawer>
