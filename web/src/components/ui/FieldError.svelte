<script lang="ts">
  import Icon from '../Icon.svelte';

  // Why the action above didn't work, announced as it appears; nothing while there is no text.
  let { text, class: cls = '' }: { text?: string | false | null; class?: string } = $props();
</script>

{#if text}<p class="field-error {cls}" role="alert"><Icon name="alert" class="mt-px" />{text}</p>{/if}
