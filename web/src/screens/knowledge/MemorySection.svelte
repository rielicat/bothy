<script lang="ts">
  import { tick } from 'svelte';
  import Markdown from '../../components/Markdown.svelte';
  import Button from '../../components/ui/Button.svelte';
  import EmptyState from '../../components/ui/EmptyState.svelte';
  import { api, ApiError } from '../../lib/api/client';
  import { fullness, isConflict } from '../../lib/api/knowledge';
  import type { MemoryDoc } from '../../lib/api/types.gen';
  import DiscardDialog from './DiscardDialog.svelte';
  import NotesEditor from './NotesEditor.svelte';

  // One memory document (§9.4): rendered Markdown, edited in place.
  let { title, doc = $bindable() }: { title: string; doc: MemoryDoc } = $props();

  const uid = $props.id();
  let editing = $state(false);
  let draft = $state('');
  let saving = $state(false);
  let triedOver = $state(false);
  let error = $state('');
  let theirs = $state<MemoryDoc | null>(null);
  let discard = $state(false);
  let status = $state<'' | 'saved' | 'applies'>('');
  // The version the edit started from: Bothy may save a newer one meanwhile, which must not be overwritten unseen.
  let base = '';
  let timer: ReturnType<typeof setTimeout> | undefined;

  const empty = $derived(!doc.content.trim());
  const size = $derived(fullness(editing ? draft : doc.content, doc.cap_bytes));

  $effect(() => () => clearTimeout(timer));

  let head: HTMLElement | undefined = $state();

  // The editor's buttons go away with it; focus lands on Edit again.
  async function leaveEdit() {
    editing = false;
    await tick();
    head?.querySelector<HTMLElement>('button')?.focus();
  }

  function edit() {
    draft = doc.content;
    base = doc.etag;
    triedOver = false;
    error = '';
    theirs = null;
    status = '';
    editing = true;
  }

  function cancel() {
    if (saving) return;
    if (draft !== doc.content) discard = true;
    else void leaveEdit();
  }

  async function save(etag = base) {
    if (saving) return;
    if (fullness(draft, doc.cap_bytes) === 'over') {
      triedOver = true;
      return;
    }
    saving = true;
    error = '';
    try {
      doc = await api.put<MemoryDoc>(`/api/memory/${doc.file}`, { content: draft }, { 'If-Match': etag });
      base = doc.etag;
      void leaveEdit();
      theirs = null;
      // "Saved" for 2 s, then what it means stays until the page is left (§9.4).
      status = 'saved';
      clearTimeout(timer);
      timer = setTimeout(() => (status = 'applies'), 2000);
    } catch (e) {
      const fresh = isConflict(e) ? await api.get<MemoryDoc>(`/api/memory/${doc.file}`).catch(() => null) : null;
      if (fresh) theirs = fresh;
      else if (e instanceof ApiError && e.status === 400 && /bytes|too long|limit/i.test(e.message)) triedOver = true;
      else error = "Couldn't save. Try again.";
    } finally {
      saving = false;
    }
  }

  function useTheirs() {
    if (!theirs) return;
    doc = theirs;
    draft = theirs.content;
    theirs = null;
    void leaveEdit();
  }
</script>

<section aria-labelledby="{uid}-h" bind:this={head}>
  <div class="flex min-h-9 flex-wrap items-center justify-between gap-x-4 gap-y-1">
    <h2 id="{uid}-h" class="section-title">{title}</h2>
    <div class="flex flex-wrap items-center justify-end gap-x-3 gap-y-1">
      {#if editing}
        <Button size="sm" variant="primary" loading={saving} loadingLabel="Saving…" onclick={() => save()}>Save</Button>
        <Button size="sm" onclick={cancel}>Cancel</Button>
      {:else}
        <span class="text-small text-muted" role="status">{status === 'saved' ? 'Saved' : status === 'applies' ? 'Changes apply from your next message.' : ''}</span>
        {#if !empty}<Button size="sm" onclick={edit}>Edit</Button>{/if}
      {/if}
    </div>
  </div>
  <div class="mt-2">
    {#if editing}
      <NotesEditor
        label={title}
        bind:value={draft}
        rows={4}
        busy={saving}
        error={triedOver && size === 'over' ? 'This is too long to save. Remove something first.' : error}
        theirs={theirs?.content ?? null}
        onsave={() => save()}
        oncancel={cancel}
        onkeepmine={() => theirs && save(theirs.etag)}
        onusetheirs={useTheirs}
      />
    {:else if empty}
      <EmptyState
        text="Bothy hasn't remembered anything yet."
        more="It adds things here as you work together, or you can write them yourself."
        action={{ label: 'Edit', onclick: edit, variant: 'secondary' }}
      />
    {:else}
      <Markdown text={doc.content} />
    {/if}
  </div>
  {#if size === 'near' || (size === 'over' && !editing)}
    <p class="mt-2 text-small text-warning-text">This is almost full. Remove anything Bothy no longer needs.</p>
  {/if}
</section>

<DiscardDialog bind:open={discard} ondiscard={leaveEdit} />
