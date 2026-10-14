<script lang="ts">
  import { tick } from 'svelte';
  import { api } from '../../lib/api/client';
  import type { Pin } from '../../lib/api/types.gen';
  import type { Conversation } from '../../screens/chat/conversation.svelte';
  import Button from '../ui/Button.svelte';
  import Drawer from '../ui/Drawer.svelte';
  import { toast } from '../ui/toast.svelte';

  let { open = $bindable(false), conv, adding = $bindable(false) }: { open?: boolean; conv: Conversation; adding?: boolean } = $props();

  let text = $state('');
  let busy = $state(false);
  let error = $state('');

  export async function pin(value: string): Promise<boolean> {
    busy = true;
    error = '';
    try {
      await conv.addPin(value);
      text = '';
      adding = false;
      toast('Pinned.');
      return true;
    } catch {
      error = "Couldn't pin that. Try again.";
      return false;
    } finally {
      busy = false;
    }
  }

  async function unpin(p: Pin) {
    error = '';
    try {
      await api.del(`${conv.base}/pins/${p.id}`);
      conv.setPins(conv.pins.filter((x) => x.id !== p.id));
    } catch {
      error = "Couldn't unpin that. Try again.";
    }
  }

  async function startAdding() {
    adding = true;
    await tick();
    document.getElementById('pin-text')?.focus();
  }
</script>

<Drawer bind:open title="Pinned to this chat" onclose={() => ((adding = false), (error = ''))}>
  <p class="text-body text-muted">Bothy keeps these in mind for every reply in this chat.</p>
  {#if conv.pins.length}
    <ul class="mt-4">
      {#each conv.pins as p (p.id)}
        <li class="row">
          <span class="min-w-0 flex-1 text-body [overflow-wrap:anywhere]" data-copy-ignore>{p.text}</span>
          <Button variant="ghost" size="sm" onclick={() => unpin(p)}>Unpin</Button>
        </li>
      {/each}
    </ul>
  {/if}
  {#if adding}
    <form
      class="mt-4 space-y-3"
      onsubmit={(e) => {
        e.preventDefault();
        if (text.trim()) void pin(text.trim());
      }}
    >
      <div>
        <label for="pin-text" class="field-label">File or note</label>
        <input id="pin-text" class="input mt-2" aria-describedby="pin-help" bind:value={text} />
        <p id="pin-help" class="field-help">A file in Bothy's files, like notes/launch.md, or a short instruction.</p>
      </div>
      <Button type="submit" variant="primary" loading={busy} loadingLabel="Pinning…">Pin</Button>
    </form>
  {:else}
    <Button class="mt-4" onclick={startAdding}>Pin a file</Button>
  {/if}
  {#if error}<p class="mt-2 text-small text-danger-text" role="alert">{error}</p>{/if}
</Drawer>
