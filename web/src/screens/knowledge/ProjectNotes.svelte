<script lang="ts">
  import { tick } from 'svelte';
  import Icon from '../../components/Icon.svelte';
  import Markdown from '../../components/Markdown.svelte';
  import Button from '../../components/ui/Button.svelte';
  import Dialog from '../../components/ui/Dialog.svelte';
  import EmptyState from '../../components/ui/EmptyState.svelte';
  import IconButton from '../../components/ui/IconButton.svelte';
  import LinkButton from '../../components/ui/LinkButton.svelte';
  import Menu, { type MenuItem } from '../../components/ui/Menu.svelte';
  import Notice from '../../components/ui/Notice.svelte';
  import PageHeader from '../../components/ui/PageHeader.svelte';
  import { toast } from '../../components/ui/toast.svelte';
  import { ApiError } from '../../lib/api/client';
  import {
    byteLength,
    failKind,
    failReason,
    fileCap,
    fullness,
    githubState,
    githubUrl,
    isConflict,
    knowledge,
    queuedRepo,
    readRepos,
    repoState,
    type KnowledgeNotes,
    type KnowledgeRepo,
    type NotesChange,
  } from '../../lib/api/knowledge';
  import { bytes, fullTime, isZeroTime, plural, relTime } from '../../lib/format';
  import { router } from '../../lib/router.svelte';
  import { events } from '../../lib/sse.svelte';
  import { approvals, session } from '../../lib/stores/session.svelte';
  import { narrow } from '../../lib/stores/viewport.svelte';
  import DiscardDialog from './DiscardDialog.svelte';
  import HeaderSpacer from './HeaderSpacer.svelte';
  import Loading from '../../components/ui/Loading.svelte';
  import NotesEditor from './NotesEditor.svelte';
  import NotesHistoryDialog, { type HistoryEntry } from './NotesHistoryDialog.svelte';
  import SuggestionsDialog from './SuggestionsDialog.svelte';
  import { changeWords, dayMonth, inSentence } from './text';

  let { params }: { params: Record<string, string> } = $props();
  const uid = $props.id();
  const name = $derived(`${params.owner}/${params.repo}`);
  const key = $derived(name.toLowerCase());

  let repo = $state<KnowledgeRepo | null>(null);
  let notes = $state<KnowledgeNotes | null>(null);
  /** null: the history didn't load, so History is hidden. */
  let history = $state<NotesChange[] | null>(null);
  let missing = $state(false);
  let failed = $state(false);

  let editing = $state(false);
  let draft = $state('');
  let saving = $state(false);
  let triedOver = $state(false);
  let saveError = $state('');
  let theirs = $state<KnowledgeNotes | null>(null);
  let comparing = $state(false);

  let discard = $state(false);
  let stop = $state(false);
  let stopping = $state(false);
  let stopError = $state('');
  let tech = $state(false);
  let showHistory = $state(false);
  let review = $state(false);
  let readError = $state('');

  const admin = $derived(session.isAdmin);
  const content = $derived(notes?.content ?? '');
  const cap = $derived(notes?.cap_bytes ?? 0);
  const st = $derived(repo ? repoState(repo) : 'ready');
  // A connected repository this person's Bothy hasn't read (no index yet): offer to read it.
  let requested = $state(false);
  const unread = $derived(!!repo && repo.id === 0 && !requested);
  const firstRead = $derived(!!repo && !unread && !content.trim() && (st === 'reading' || repo.notes.status === 'drafting'));

  async function readNow() {
    if (!repo) return;
    readError = '';
    requested = true;
    try {
      await knowledge.readConnected([repo.repo]);
    } catch {
      requested = false;
      readError = "Couldn't start reading. Try again.";
    }
  }
  const size = $derived(fullness(editing ? draft : content, cap));
  const over = $derived(editing && size === 'over');
  // Shown under the header, next to Save: below a long document it would be off screen on a phone.
  const editError = $derived(editing ? (triedOver && over ? 'These notes are too long to save. Remove something first.' : saveError) : '');

  const suggestions = $derived(
    approvals.pending.filter((a) => a.action?.tool === 'notes' && String((a.action.payload as { repo?: string } | null)?.repo ?? '').toLowerCase() === key),
  );

  async function load() {
    failed = false;
    try {
      let r = (await readRepos()).find((x) => x.repo.toLowerCase() === key);
      // Suggestions are approvals; refresh them so the notice is current on arrival.
      void approvals.load();
      // Chosen on GitHub but not read yet: the page shows the first read.
      if (!r && (await githubState(false)).allowed.includes(key)) r = queuedRepo(key);
      if (!r) {
        missing = true;
        return;
      }
      repo = r;
      if (r.id) [notes, history] = await Promise.all([knowledge.notes(r.id), knowledge.history(r.id).catch(() => null)]);
    } catch {
      failed = true;
    }
  }

  $effect(() => {
    void key;
    repo = notes = history = theirs = null;
    missing = editing = false;
    void load();
    return events.on('knowledge.updated', (e) => {
      const d = e.data as { repo: KnowledgeRepo; deleted?: boolean };
      if (d?.repo?.repo.toLowerCase() !== key) return;
      if (d.deleted) {
        missing = true;
        return;
      }
      const notesMoved = repo?.notes.updated_at !== d.repo.notes.updated_at;
      repo = d.repo;
      if (notesMoved && !editing && repo.id) {
        void knowledge.notes(repo.id).then((n) => (notes = n), () => {});
        void knowledge.history(repo.id).then((h) => (history = h), () => {});
      }
    });
  });

  let bar: HTMLElement | undefined = $state();

  // The editor's buttons go away with it; focus lands on the first header action again.
  async function leaveEdit() {
    editing = false;
    await tick();
    bar?.querySelector<HTMLElement>('button, a[href]')?.focus();
  }

  function startEdit() {
    draft = content;
    triedOver = false;
    saveError = '';
    theirs = null;
    editing = true;
  }

  function cancel() {
    if (saving) return;
    if (draft !== content) discard = true;
    else void leaveEdit();
  }

  async function save(etag = notes?.etag ?? '') {
    if (!repo?.id || saving) return;
    if (fullness(draft, cap) === 'over') {
      triedOver = true;
      return;
    }
    saving = true;
    saveError = '';
    try {
      notes = await knowledge.saveNotes(repo.id, draft, etag);
      void leaveEdit();
      theirs = null;
      toast('Saved.');
      history = await knowledge.history(repo.id).catch(() => history);
    } catch (e) {
      const fresh = isConflict(e) ? await knowledge.notes(repo.id).catch(() => null) : null;
      if (fresh) theirs = fresh;
      else if (e instanceof ApiError && e.status === 403) saveError = "You can't do this. Ask an admin.";
      else if (e instanceof ApiError && e.status === 400 && /bytes|too long|limit/i.test(e.message)) triedOver = true;
      else saveError = "Couldn't save. Try again.";
    } finally {
      saving = false;
    }
  }

  function useTheirs() {
    if (!theirs) return;
    notes = theirs;
    draft = theirs.content;
    theirs = null;
    void leaveEdit();
  }

  function windowKey(e: KeyboardEvent) {
    // Keys inside a dialog (their version, discard) belong to the dialog.
    if (!editing || e.defaultPrevented || (e.target instanceof Element && e.target.closest('dialog'))) return;
    if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 's') {
      e.preventDefault();
      void save();
    } else if (e.key === 'Escape') cancel();
  }

  async function readAgain() {
    if (!repo?.id) return;
    readError = '';
    try {
      repo = await knowledge.reindex(repo.id);
      toast('Started reading again.');
    } catch {
      readError = "Couldn't start reading this repository again. Try again.";
    }
  }

  async function stopReading() {
    if (!repo?.id) return;
    stopping = true;
    stopError = '';
    try {
      await knowledge.stop(repo.id);
      stop = false;
      toast(`Stopped reading ${name}.`);
      router.navigate('/knowledge');
    } catch {
      stopError = "Couldn't stop reading it. Try again.";
    } finally {
      stopping = false;
    }
  }

  const menu: MenuItem[] = $derived([
    { label: 'Ask Bothy about this project', onselect: () => router.navigate(`/chat?prompt=${encodeURIComponent(`About ${name}: `)}`) },
    ...(admin ? [{ label: 'Read the repository again', onselect: readAgain }] : []),
    { label: 'View on GitHub ↗', onselect: () => window.open(githubUrl(name), '_blank', 'noopener,noreferrer') },
    { label: 'Technical details', onselect: () => (tech = true) },
    ...(admin ? (['separator', { label: 'Stop reading this repository…', danger: true, onselect: () => (stop = true) }] as MenuItem[]) : []),
  ]);

  const whoOf = (c: NotesChange, mid = false) =>
    c.actor === 'bothy' ? 'Bothy' : c.user_id && c.user_id === session.me?.user.id ? (mid ? 'you' : 'You') : c.name || (mid ? 'someone else' : 'Someone else');

  const byline = $derived.by(() => {
    if (!notes || !content.trim()) return '';
    if (!history?.length) return isZeroTime(notes.updated_at) ? '' : `Notes updated ${inSentence(relTime(notes.updated_at))}.`;
    const byAge = [...history].sort((a, b) => a.at.localeCompare(b.at));
    const first = byAge[0];
    const last = byAge[byAge.length - 1];
    const parts = [`Written by ${whoOf(first)} on ${dayMonth(first.at)}`];
    if (last !== first) parts.push(`${last.actor === 'bothy' ? 'Updated' : 'Edited'} by ${whoOf(last, true)} ${inSentence(relTime(last.at))}`);
    return parts.join(' · ');
  });

  const entries: HistoryEntry[] | null = $derived(
    history?.map((c) => ({ key: c.id, who: whoOf(c), what: changeWords(c.before, c.after), at: c.at, chat: c.chat, before: c.before, after: c.after })) ?? null,
  );
</script>

<svelte:head><title>{name} · Bothy</title></svelte:head>
<svelte:window onkeydown={windowKey} />

<!-- Saving again during a conflict would only meet it again, so the primary opens the comparison instead. -->
{#snippet editButtons()}
  {#if theirs}
    <Button variant="primary" onclick={() => (comparing = true)}>Compare versions</Button>
  {:else}
    <Button variant="primary" loading={saving} loadingLabel="Saving…" onclick={() => save()}>Save</Button>
  {/if}
  <Button onclick={cancel}>Cancel</Button>
{/snippet}

{#snippet actions()}
  <span class="contents" bind:this={bar}>
  <HeaderSpacer />
  {#if editing}
    <!-- On phones these sit under the text box instead, where focusing it doesn't scroll them away. -->
    {#if !narrow.current}{@render editButtons()}{/if}
  {:else if repo?.id}
    <!-- Empty notes offer "Write notes" in the page instead. -->
    {#if content.trim()}<Button onclick={startEdit}>Edit</Button>{/if}
    <Menu items={menu} placement="bottom-end">
      {#snippet trigger(p)}<IconButton {...p} icon="more" label="Project options" />{/snippet}
    </Menu>
  {/if}
  </span>
{/snippet}

<div class="page">
  <PageHeader title={name} back={{ href: '/knowledge', label: 'Knowledge' }} actions={missing ? undefined : actions} />
  {#if editError}
    <p id="{uid}-error" class="field-error mt-2" role="alert"><Icon name="alert" class="mt-px" /><span class="min-w-0">{editError}</span></p>
  {:else if size === 'near' || size === 'over'}
    <p class="mt-2 text-small text-warning-text">These notes are almost full. Shorten them so Bothy has room to add more.</p>
  {/if}

  <div class="mt-6">
    {#if missing}
      <EmptyState text="Bothy doesn't read this repository." action={{ label: 'Go to Knowledge', href: '/knowledge', variant: 'secondary' }} />
    {:else if !repo || (repo.id && !notes)}
      <Loading {failed} retry={load} />
    {:else}
      {#if readError}
        <Notice tone="danger" class="mb-6" text={readError} />
      {/if}
      {#if st === 'failed' && failKind(repo) === 'size'}
        <!-- Reading again would fail the same way, so there's nothing to offer but notes written by hand. -->
        <Notice tone="danger" class="mb-6" text="This repository is too big for Bothy to read{fileCap(repo) ? ` (${fileCap(repo)})` : ''}." />
      {:else if st === 'failed'}
        <!-- The reason says what happened in a sentence of its own; only an unexplained failure needs the general one. -->
        <Notice
          tone="danger"
          class="mb-6"
          text={failReason(repo, "Bothy couldn't read this repository.")}
          action={!admin
            ? undefined
            : failKind(repo) === 'temporary'
              ? { label: 'Read again', onclick: readAgain }
              : failKind(repo) === 'access'
                ? { label: 'Check the GitHub connection', onclick: () => router.navigate('/settings/connections/github') }
                : undefined}
        />
      {/if}
      {#if suggestions.length && !editing}
        <Notice class="mb-6" text="Bothy suggested {plural(suggestions.length, 'change')} to these notes." action={{ label: 'Review', onclick: () => (review = true) }} />
      {/if}

      {#if editing}
        <NotesEditor
          label="Notes for {name}"
          bind:value={draft}
          busy={saving}
          error={editError}
          errorId="{uid}-error"
          theirs={theirs?.content ?? null}
          conflictText="Someone else changed these notes while you were editing."
          bind:comparing
          actions={narrow.current ? editButtons : undefined}
          onsave={() => save()}
          oncancel={cancel}
          onkeepmine={() => theirs && save(theirs.etag)}
          onusetheirs={useTheirs}
        />
      {:else if content.trim()}
        <Markdown text={content} />
      {:else if unread}
        <EmptyState
          text="Bothy hasn't read {name} for you yet."
          more="It reads the code and writes notes, so you can ask about this project."
          action={{ label: 'Read now', onclick: readNow }}
        />
      {:else if firstRead}
        <p class="text-reading text-text">Bothy is reading {name} and writing notes. This usually takes a few minutes.</p>
        {#if repo.progress?.total}
          <p class="text-reading text-muted tabular-nums">Read {repo.progress.done.toLocaleString()} of about {repo.progress.total.toLocaleString()} files</p>
        {/if}
      {:else if st === 'failed'}
        <!-- Not "Bothy writes them after reading": it can't read this one. -->
        <EmptyState text="You can write notes for this project yourself." action={{ label: 'Write notes', onclick: startEdit, variant: 'secondary' }} />
      {:else}
        <EmptyState
          text="There are no notes for this project yet."
          more="Bothy writes them after reading the repository, or you can write them yourself."
          action={{ label: 'Write notes', onclick: startEdit, variant: 'secondary' }}
        />
      {/if}

      {#if byline && !editing}
        <p class="mt-10 text-small text-muted">
          <span title={fullTime(notes?.updated_at)}>{byline}</span>{#if entries}{' · '}<LinkButton onclick={() => (showHistory = true)}>History</LinkButton>{/if}
        </p>
      {/if}
    {/if}
  </div>
</div>

<DiscardDialog bind:open={discard} ondiscard={leaveEdit} />

<Dialog bind:open={stop} title="Stop reading {name}?" initialFocus="cancel" busy={stopping}>
  <!-- The server deletes the notes with the index: keeping them would bring them back unmarked as outside content if the repository is read again. -->
  <p class="text-body text-text">Bothy won't use {name} to answer questions, and its project notes and their history will be deleted. This can't be undone.</p>
  {#if stopError}<p class="field-error">{stopError}</p>{/if}
  {#snippet footer({ close })}
    <Button onclick={close}>Cancel</Button>
    <Button variant="danger" loading={stopping} loadingLabel="Stopping…" onclick={stopReading}>Stop reading</Button>
  {/snippet}
</Dialog>

<Dialog bind:open={tech} title="Technical details">
  {#if repo}
    <dl class="grid grid-cols-[minmax(0,9rem)_minmax(0,1fr)] gap-x-4 gap-y-2 text-body" data-details>
      <dt class="text-muted">Commit read</dt>
      <!-- indexed_sha, not sha: "Read again" asks for the branch head, so the requested commit is "" until a checkout names one. -->
      <dd>{#if repo.indexed_sha}<span class="font-mono text-code" data-path>{repo.indexed_sha.slice(0, 7)}</span>{:else}Not read yet{/if}</dd>
      <dt class="text-muted">Files indexed</dt>
      <dd class="tabular-nums">{repo.files.toLocaleString()}</dd>
      {#if repo.skipped}
        <dt class="text-muted">Files left out</dt>
        <dd class="tabular-nums">{repo.skipped.toLocaleString()}</dd>
      {/if}
      <dt class="text-muted">Last read</dt>
      <dd title={fullTime(repo.indexed_at)}>{isZeroTime(repo.indexed_at) ? 'Not read yet' : relTime(repo.indexed_at)}</dd>
      <dt class="text-muted">Notes size</dt>
      <dd class="tabular-nums">{bytes(byteLength(content))} of {bytes(cap)}</dd>
    </dl>
  {/if}
</Dialog>

<NotesHistoryDialog bind:open={showHistory} {entries} />
<SuggestionsDialog bind:open={review} items={suggestions} />
