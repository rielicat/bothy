<script lang="ts">
  import Empty from '../../components/Empty.svelte';
  import Icon from '../../components/Icon.svelte';
  import { api } from '../../lib/api/client';
  import type { Session } from '../../lib/api/types.gen';
  import { router } from '../../lib/router.svelte';
  import { toasts } from '../../lib/stores/ui.svelte';
  import ConversationView from './ConversationView.svelte';
  import Sidebar from './Sidebar.svelte';

  let { params }: { params: Record<string, string> } = $props();

  const id = $derived(params.id ?? '');

  async function start() {
    try {
      const s = await api.post<Session>('/api/sessions', {});
      router.navigate(`/chat/${encodeURIComponent(s.id)}`);
    } catch (e) {
      toasts.error(e);
    }
  }
</script>

<svelte:head><title>Chat · Bothy</title></svelte:head>

<div class="flex h-full">
  {#if !id}<h1 class="sr-only">Chats</h1>{/if}
  <aside class="w-full shrink-0 border-r border-line bg-surface lg:block lg:w-72 {id ? 'hidden' : ''}" aria-label="Chats">
    <Sidebar activeId={id} />
  </aside>
  <section class="min-w-0 flex-1 {id ? '' : 'hidden lg:block'}" aria-label="Conversation">
    {#if id}
      {#key id}<ConversationView {id} />{/key}
    {:else}
      <div class="grid h-full place-items-center">
        <Empty icon="chat" title="Pick a chat or start a new one">
          Chats from Telegram, Discord and Slack show up here too.
          {#snippet action()}<button type="button" class="btn btn-primary" onclick={start}><Icon name="plus" size={16} />New chat</button>{/snippet}
        </Empty>
      </div>
    {/if}
  </section>
</div>
