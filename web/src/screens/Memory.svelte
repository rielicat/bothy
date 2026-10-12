<script lang="ts">
  import DiffView from '../components/DiffView.svelte';
  import Empty from '../components/Empty.svelte';
  import Icon from '../components/Icon.svelte';
  import { api, ApiError, errorText } from '../lib/api/client';
  import type { List, MemoryData, MemoryDoc, MemoryEvent, MemoryFile } from '../lib/api/types.gen';
  import { lineDiff } from '../lib/diff';
  import { ago } from '../lib/format';
  import { router } from '../lib/router.svelte';
  import { events } from '../lib/sse.svelte';
  import { clock } from '../lib/stores/clock.svelte';
  import { toasts } from '../lib/stores/ui.svelte';

  const files: { f: MemoryFile; label: string; help: string }[] = [
    { f: 'MEMORY', label: 'Memory', help: 'Facts and preferences Bothy keeps across chats. Bothy adds to it as you talk; you can edit anything.' },
    { f: 'USER', label: 'About you', help: 'Who you are and how you like to work. Short and factual works best.' },
    { f: 'HEARTBEAT', label: 'Heartbeat', help: 'A checklist Bothy reads on each heartbeat. Leave it empty and heartbeats cost nothing.' },
  ];

  const file = $derived((files.find((x) => x.f === router.query.get('file'))?.f ?? 'MEMORY') as MemoryFile);
  let doc = $state<MemoryDoc | null>(null);
  let text = $state('');
  let history = $state<MemoryEvent[]>([]);
  let error = $state('');
  let saving = $state(false);
  let conflict = $state<MemoryDoc | null>(null);
  let changedElsewhere = $state(false);

  const size = $derived(new TextEncoder().encode(text).length);
  const over = $derived(!!doc && size > doc.cap_bytes);
  const dirty = $derived(!!doc && text !== doc.content);
  const info = $derived(files.find((x) => x.f === file)!);

  async function load() {
    error = '';
    conflict = null;
    changedElsewhere = false;
    try {
      const [d, h] = await Promise.all([api.get<MemoryDoc>(`/api/memory/${file}`), api.get<List<MemoryEvent>>(`/api/memory/${file}/history?limit=25`)]);
      doc = d;
      text = d.content;
      history = h.items ?? [];
    } catch (e) {
      error = errorText(e);
    }
  }

  $effect(() => {
    void file;
    void load();
  });

  $effect(() => {
    const off = events.on('memory.updated', (e) => {
      if ((e.data as MemoryData).file !== file) return;
      if (dirty) changedElsewhere = true;
      else void load();
    });
    return off;
  });

  async function save(etag = doc?.etag ?? '') {
    saving = true;
    try {
      doc = await api.put<MemoryDoc>(`/api/memory/${file}`, { content: text }, { 'If-Match': etag });
      text = doc.content;
      conflict = null;
      changedElsewhere = false;
      toasts.ok('Saved. Changes appear in your next message.');
      history = (await api.get<List<MemoryEvent>>(`/api/memory/${file}/history?limit=25`).catch(() => null))?.items ?? history;
    } catch (e) {
      if (e instanceof ApiError && (e.status === 409 || e.status === 412)) conflict = await api.get<MemoryDoc>(`/api/memory/${file}`).catch(() => null);
      else toasts.error(e);
    } finally {
      saving = false;
    }
  }

  const actor = (a: string) => (a.startsWith('agent:') ? { label: 'Bothy', run: a.slice(6) } : { label: a.startsWith('user:') ? 'You or an admin' : a, run: '' });
</script>

<svelte:head><title>Memory · Bothy</title></svelte:head>

<div class="page max-w-4xl">
  <div class="mb-6 flex flex-wrap items-end gap-3">
    <div class="flex-1">
      <h1 class="page-title">Memory</h1>
      <p class="mt-1 text-sm text-muted">What Bothy remembers about you. Changes appear in your next message.</p>
    </div>
    <a class="btn btn-sm" href="/api/memory/export" download><Icon name="download" size={14} />Export</a>
  </div>

  <div class="tabs mb-5" role="tablist" aria-label="Memory files">
    {#each files as x (x.f)}
      <button type="button" role="tab" class="tab" aria-selected={file === x.f} onclick={() => router.setQuery({ file: x.f === 'MEMORY' ? null : x.f })}>
        {x.label}
      </button>
    {/each}
  </div>

  {#if error}
    <p class="alert-bad" role="alert">{error}</p>
  {:else if doc}
    <p class="mb-3 text-sm text-muted">{info.help}</p>
    {#if conflict}
      <div class="alert-warn mb-3 flex-col">
        <p class="font-medium">This file changed while you were editing.</p>
        <DiffView diff={lineDiff(conflict.content, text)} />
        <div class="flex flex-wrap gap-2">
          <button type="button" class="btn btn-sm" onclick={() => ((doc = conflict), (text = conflict!.content), (conflict = null))}>Discard mine, load theirs</button>
          <button type="button" class="btn btn-sm btn-primary" onclick={() => save(conflict!.etag)}>Keep mine</button>
        </div>
      </div>
    {:else if changedElsewhere}
      <p class="alert-info mb-3">Bothy just updated this file. Saving will show both versions so nothing is lost.</p>
    {/if}
    <label for="mem-text" class="sr-only">{info.label}</label>
    <textarea id="mem-text" class="input mono min-h-72" spellcheck="true" bind:value={text}></textarea>
    <div class="mt-2 flex flex-wrap items-center gap-3">
      <p class="text-xs tabular-nums {over ? 'font-medium text-bad' : 'text-muted'}" aria-live="polite">
        {size.toLocaleString()} / {doc.cap_bytes.toLocaleString()} bytes{over ? ' · over the limit' : ''}
      </p>
      <span class="h-1.5 w-28 overflow-hidden rounded-full bg-sunken" aria-hidden="true">
        <span class="block h-full {over ? 'bg-bad' : size / doc.cap_bytes > 0.8 ? 'bg-warn' : 'bg-accent'}" style:width="{Math.min(100, (size / doc.cap_bytes) * 100)}%"></span>
      </span>
      <div class="flex-1"></div>
      {#if dirty}<button type="button" class="btn btn-sm btn-ghost" onclick={() => (text = doc!.content)}>Revert</button>{/if}
      <button type="button" class="btn btn-primary btn-sm" disabled={!dirty || over || saving} onclick={() => save()}>{saving ? 'Saving…' : 'Save'}</button>
    </div>

    <h2 class="mt-10 mb-3 section-title">History</h2>
    {#if !history.length}
      <Empty icon="clock" title="No changes yet">Edits by you and by Bothy are listed here with what changed.</Empty>
    {:else}
      <ol class="space-y-3">
        {#each history as ev (ev.id)}
          {@const who = actor(ev.actor)}
          <li class="card p-4">
            <div class="mb-2 flex flex-wrap items-center gap-2 text-sm">
              <span class="chip">{ev.op}</span>
              <span class="font-medium">{who.label}</span>
              <span class="chip {ev.provenance === 'user' || ev.provenance === 'system' ? '' : 'chip-warn'}" title="Who authored the content that led to this change">{ev.provenance}</span>
              {#if who.run}<a class="link text-xs" href="/runs/{encodeURIComponent(who.run)}" onclick={router.link}>run</a>{/if}
              <span class="ml-auto text-xs text-faint">{ago(ev.at, clock.now)}</span>
            </div>
            <DiffView diff={lineDiff(ev.before, ev.after)} />
          </li>
        {/each}
      </ol>
    {/if}
  {:else}
    <div class="skeleton h-72"></div>
  {/if}
</div>
