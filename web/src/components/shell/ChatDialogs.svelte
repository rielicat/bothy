<script lang="ts">
  import { untrack } from 'svelte';
  import { api } from '../../lib/api/client';
  import { router } from '../../lib/router.svelte';
  import Button from '../ui/Button.svelte';
  import Dialog from '../ui/Dialog.svelte';
  import TextField from '../ui/TextField.svelte';
  import { toast } from '../ui/toast.svelte';
  import { chats } from './chats.svelte';

  // Rename and Delete from a chat row's ⋯ (§4.2) and Delete from the chat menu (§7.7).
  const uid = $props.id();
  let name = $state('');
  // Kept after `deleting` clears so the words don't change while the dialog closes.
  let deletingTitle = $state('');
  let continued = $state(false);
  let busy = $state(false);
  let error = $state('');

  $effect(() => {
    const s = chats.renaming ?? chats.deleting;
    error = '';
    if (s) name = chats.title(s);
    const d = chats.deleting;
    if (!d) return;
    deletingTitle = chats.title(d);
    continued = untrack(() => chats.list.some((x) => x.parent_id === d.id || x.root_id === d.id));
  });

  async function rename(e: SubmitEvent) {
    e.preventDefault();
    const s = chats.renaming;
    const title = name.trim();
    if (!s) return;
    if (!title) {
      error = 'Enter a name.';
      return;
    }
    busy = true;
    try {
      await api.patch(`/api/sessions/${encodeURIComponent(s.id)}`, { title });
      chats.rename(s.id, title);
      busy = false;
      chats.renaming = null;
      toast('Chat renamed.');
    } catch {
      error = "Couldn't save. Try again.";
    } finally {
      busy = false;
    }
  }

  async function remove() {
    const s = chats.deleting;
    if (!s) return;
    busy = true;
    try {
      await api.del(`/api/sessions/${encodeURIComponent(s.id)}`);
      chats.remove(s.id);
      busy = false;
      chats.deleting = null;
      if (router.current?.pattern === '/chat/:id' && router.current.params.id === s.id) router.navigate('/chat');
      toast('Chat deleted.');
    } catch {
      error = "Couldn't delete the chat. Try again.";
    } finally {
      busy = false;
    }
  }
</script>

<Dialog bind:open={() => !!chats.renaming, (v) => !v && (chats.renaming = null)} title="Rename chat" {busy} dirty>
  <form id="{uid}-rename" onsubmit={rename}>
    <TextField label="Name" bind:value={name} {error} maxlength={200} autocomplete="off" />
  </form>
  {#snippet footer({ close })}
    <Button data-cancel onclick={close}>Cancel</Button>
    <Button variant="primary" type="submit" form="{uid}-rename" loading={busy} loadingLabel="Saving…">Save</Button>
  {/snippet}
</Dialog>

<Dialog bind:open={() => !!chats.deleting, (v) => !v && (chats.deleting = null)} title="Delete “{deletingTitle}”?" initialFocus="cancel" {busy}>
  <p class="text-body text-text">
    {continued ? 'The messages will be deleted; the chats continued from it stay in your list.' : 'The messages will be deleted.'} Anything Bothy already did stays done, and its record stays in Activity. This can't be undone.
  </p>
  {#if error}<p class="mt-3 text-small text-danger-text" role="alert">{error}</p>{/if}
  {#snippet footer({ close })}
    <Button data-cancel onclick={close}>Cancel</Button>
    <Button variant="danger" loading={busy} loadingLabel="Deleting…" onclick={remove}>Delete chat</Button>
  {/snippet}
</Dialog>
