<script lang="ts">
  import IconButton from '../IconButton.svelte';
  import Menu, { type MenuItem } from '../Menu.svelte';

  let { items }: { items: MenuItem[] } = $props();
</script>

<Menu {items}>
  {#snippet trigger(props)}<IconButton icon="more" label="Chat options" {...props} />{/snippet}
</Menu>
<button type="button">Elsewhere</button>
