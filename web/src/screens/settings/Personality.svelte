<script lang="ts">
  import { onMount } from 'svelte';
  import { Button, Dialog, DiffView, Notice, StatusLine, TextArea, toast } from '../../components/ui';
  import { api } from '../../lib/api/client';
  import { lineDiff } from '../../lib/diff';
  import { router } from '../../lib/router.svelte';
  import ConfirmDialog from './ConfirmDialog.svelte';
  import SettingsFrame from './SettingsFrame.svelte';

  // The server's cap on the personality file, in bytes.
  const MAX = 16384;
  const back = '/settings/general';

  let original = $state<string | null>(null);
  let text = $state('');
  let failed = $state(false);
  let saving = $state(false);
  let saveError = $state('');
  let theirs = $state<string | null>(null);
  let reviewOpen = $state(false);
  let discardOpen = $state(false);

  const size = $derived(new TextEncoder().encode(text).length);
  const dirty = $derived(original !== null && text !== original);

  async function load() {
    failed = false;
    try {
      original = text = (await api.get<{ content: string }>('/api/persona')).content;
    } catch {
      failed = true;
    }
  }

  onMount(() => void load());

  async function save() {
    if (saving || original === null) return;
    if (size > MAX) {
      saveError = 'The personality is too long to save. Remove something first.';
      return;
    }
    saving = true;
    saveError = '';
    try {
      // The personality has no version yet, so a fresh read stands in for If-Match.
      const now = (await api.get<{ content: string }>('/api/persona')).content;
      if (now !== original) {
        theirs = now;
        return;
      }
      await api.put('/api/persona', { content: text });
      original = text;
      toast('Saved.');
      router.navigate(back);
    } catch {
      saveError = "Couldn't save. Try again.";
    } finally {
      saving = false;
    }
  }

  function keepMine() {
    original = theirs;
    theirs = null;
    reviewOpen = false;
    void save();
  }

  function useTheirs() {
    if (theirs !== null) original = text = theirs;
    theirs = null;
    reviewOpen = false;
  }

  function cancel() {
    if (dirty) discardOpen = true;
    else router.navigate(back);
  }

  function keydown(e: KeyboardEvent) {
    if (reviewOpen || discardOpen) return;
    if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 's') {
      e.preventDefault();
      void save();
    } else if (e.key === 'Escape' && !e.defaultPrevented) {
      e.preventDefault();
      cancel();
    }
  }
</script>

<svelte:window onkeydown={keydown} />

<SettingsFrame title="Personality" back={{ href: back, label: 'General' }} loading={original === null && !failed}>
  {#snippet actions()}
    {#if original !== null}
      <Button variant="primary" loading={saving} loadingLabel="Saving…" onclick={save}>Save</Button>
      <Button onclick={cancel} disabled={saving}>Cancel</Button>
    {/if}
  {/snippet}

  {#if failed}
    <StatusLine tone="danger" text="This page didn't load. Check your connection and try again." action={{ label: 'Try again', onclick: load }} />
  {:else}
    {#if theirs !== null}
      <Notice class="mb-4" tone="warning" text="Someone else changed the personality while you were editing." action={{ label: 'See their version', onclick: () => (reviewOpen = true) }} />
    {/if}
    <TextArea label="Personality" class="[&>div:first-child]:sr-only [&>textarea]:mt-0" rows={16} bind:value={text} error={saveError || undefined} />
    {#if size > MAX * 0.8 && !saveError}
      <p class="mt-2 text-small text-warning-text">The personality is almost full. Shorten it so there's room for more.</p>
    {/if}
  {/if}
</SettingsFrame>

<Dialog bind:open={reviewOpen} title="Their version" size="lg">
  <p class="mb-3 text-body text-muted">What changed since you started editing.</p>
  <DiffView mode="prose" diff={lineDiff(original ?? '', theirs ?? '')} />
  {#snippet footer()}
    <Button onclick={keepMine}>Keep mine</Button>
    <Button onclick={useTheirs}>Use theirs</Button>
  {/snippet}
</Dialog>

<ConfirmDialog
  bind:open={discardOpen}
  title="Discard your changes?"
  cancel="Keep editing"
  confirm="Discard"
  onconfirm={() => {
    discardOpen = false;
    router.navigate(back);
  }}>Your edits to the personality will be lost.</ConfirmDialog
>
