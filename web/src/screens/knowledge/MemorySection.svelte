<script lang="ts">
  import { tick } from 'svelte';
  import Markdown from '../../components/Markdown.svelte';
  import Button from '../../components/ui/Button.svelte';
  import { api, ApiError } from '../../lib/api/client';
  import { fullness, isConflict, memoryUrl } from '../../lib/api/knowledge';
  import type { MemoryDoc } from '../../lib/api/types.gen';
  import { narrow } from '../../lib/stores/viewport.svelte';
  import DiscardDialog from './DiscardDialog.svelte';
  import NotesEditor from './NotesEditor.svelte';

  // One memory document (§9.4): rendered Markdown, edited in place. `empty`
  // says what the section is for until something is in it; `onsaved` tells the
  // page its history isn't empty any more. `readonly` is a section the reader
  // may only read (what's remembered for everyone, for members).
  let {
    title,
    doc = $bindable(),
    empty: emptyText,
    note = '',
    readonly = false,
    applies = 'Changes apply from your next message.',
    onsaved,
  }: { title: string; doc: MemoryDoc; empty: string; note?: string; readonly?: boolean; applies?: string; onsaved?: () => void } = $props();

  const uid = $props.id();
  let editing = $state(false);
  let draft = $state('');
  let saving = $state(false);
  let triedOver = $state(false);
  let error = $state('');
  let theirs = $state<MemoryDoc | null>(null);
  let comparing = $state(false);
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
      doc = await api.put<MemoryDoc>(memoryUrl(doc.file), { content: draft }, { 'If-Match': etag });
      base = doc.etag;
      void leaveEdit();
      theirs = null;
      onsaved?.();
      // "Saved" for 2 s, then what it means stays until the page is left (§9.4).
      status = 'saved';
      clearTimeout(timer);
      timer = setTimeout(() => (status = 'applies'), 2000);
    } catch (e) {
      const fresh = isConflict(e) ? await api.get<MemoryDoc>(memoryUrl(doc.file)).catch(() => null) : null;
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

<!-- Saving again during a conflict would only meet it again, so the primary opens the comparison instead. -->
{#snippet editButtons()}
  {#if theirs}
    <Button size="sm" variant="primary" onclick={() => (comparing = true)}>Compare versions</Button>
  {:else}
    <Button size="sm" variant="primary" loading={saving} loadingLabel="Saving…" onclick={() => save()}>Save</Button>
  {/if}
  <Button size="sm" onclick={cancel}>Cancel</Button>
{/snippet}

<section aria-labelledby="{uid}-h" aria-describedby={note ? `${uid}-note` : undefined} bind:this={head}>
  <!-- The status sits next to Edit; on phones it takes its own line under the heading so Edit never moves.
       It stays mounted while editing so the live region exists before "Saved" is written into it. -->
  <div class="flex min-h-9 flex-wrap items-center gap-x-3 pointer-coarse:min-h-11">
    <h2 id="{uid}-h" class="section-title min-w-0 flex-1">{title}</h2>
    <span class="text-small text-muted max-sm:order-last max-sm:w-full" role="status">{status === 'saved' ? 'Saved' : status === 'applies' ? applies : ''}</span>
    {#if editing}
      <!-- On phones these sit under the text box instead, where focusing it doesn't scroll them away. -->
      {#if !narrow.current}<div class="ml-auto flex items-center gap-3">{@render editButtons()}</div>{/if}
    {:else if !readonly}
      <!-- An empty section offers "Write" in the same spot as Edit, so the button never moves. -->
      <Button size="sm" class="ml-auto" onclick={edit}>{empty ? 'Write' : 'Edit'}</Button>
    {/if}
  </div>
  {#if note}<p id="{uid}-note" class="text-small text-muted">{note}</p>{/if}
  <div class="mt-2">
    {#if editing}
      <NotesEditor
        label={title}
        bind:value={draft}
        rows={4}
        busy={saving}
        error={triedOver && size === 'over' ? 'This is too long to save. Remove something first.' : error}
        theirs={theirs?.content ?? null}
        bind:comparing
        actions={narrow.current ? editButtons : undefined}
        onsave={() => save()}
        oncancel={cancel}
        onkeepmine={() => theirs && save(theirs.etag)}
        onusetheirs={useTheirs}
      />
    {:else if empty}
      <p class="text-body text-muted">{emptyText}</p>
    {:else}
      <Markdown text={doc.content} />
    {/if}
  </div>
  {#if !readonly && (size === 'near' || (size === 'over' && !editing))}
    <p class="mt-2 text-small text-warning-text">This is almost full. Remove anything Bothy no longer needs.</p>
  {/if}
</section>

<DiscardDialog bind:open={discard} ondiscard={leaveEdit} />
