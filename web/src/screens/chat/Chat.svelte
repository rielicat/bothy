<script lang="ts">
  import { untrack } from 'svelte';
  import LiveRegion from '../../components/ui/LiveRegion.svelte';
  import ToastHost from '../../components/ui/ToastHost.svelte';
  import ConversationView from './ConversationView.svelte';

  // /chat is a new, unsaved chat; /chat/:id a stored one (§7.2). A new chat
  // keeps its view when its first message gives it an id, so the reply that
  // is already streaming is not lost to a remount.
  let { params }: { params: Record<string, string> } = $props();

  const id = $derived(params.id ?? '');
  let created = '';
  let shown = untrack(() => id);
  let key = $state(0);

  $effect.pre(() => {
    const next = id;
    untrack(() => {
      if (next !== shown && !(next && next === created)) key++;
      shown = next;
      created = '';
    });
  });
</script>

<svelte:head><title>Chat · Bothy</title></svelte:head>

{#key key}
  <ConversationView id={untrack(() => id)} oncreated={(n) => (created = n)} />
{/key}

<!-- Until the v0.2 shell mounts its own (C1); only the first one mounted shows anything. -->
<ToastHost />
<LiveRegion />
