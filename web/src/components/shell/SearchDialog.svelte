<script lang="ts">
  import { tick } from 'svelte';
  import { api, qs } from '../../lib/api/client';
  import type { SearchHit } from '../../lib/api/types.gen';
  import { plural } from '../../lib/format';
  import { router } from '../../lib/router.svelte';
  import { clock } from '../../lib/stores/clock.svelte';
  import { shell } from '../../lib/stores/ui.svelte';
  import Icon from '../Icon.svelte';
  import IconButton from '../ui/IconButton.svelte';
  import { announce } from '../ui/live.svelte';
  import { trapTab } from '../ui/layer';
  import { chats } from './chats.svelte';
  import { dayLabel, highlight, plainSnippet } from './search';

  // docs/design/ui.md §6.3. Only chats until Knowledge has a search API
  // (S18), so the Notes and Code groups and the Knowledge link stay out.
  interface Row {
    id: string;
    href: string;
    title: string;
    snippet?: string;
    at: string;
  }

  const uid = $props.id();
  let el: HTMLDialogElement | undefined = $state();
  let input: HTMLInputElement | undefined = $state();
  let opener: HTMLElement | null = null;
  let q = $state('');
  let hits: SearchHit[] | null = $state(null);
  let active = $state(0);
  let seq = 0;

  const term = $derived(q.trim());
  const enc = encodeURIComponent;

  const rows: Row[] = $derived.by(() => {
    if (!term) return chats.visible.slice(0, 5).map((s) => ({ id: s.id, href: `/chat/${enc(s.id)}`, title: chats.title(s), at: s.last_message_at }));
    if (!hits) return [];
    const first = new Map<string, SearchHit>();
    for (const h of hits) if (!first.has(h.session_id)) first.set(h.session_id, h);
    const out: Row[] = [];
    const want = term.toLowerCase();
    for (const s of chats.visible) {
      if (out.length === 5) break;
      const h = first.get(s.id);
      if (chats.title(s).toLowerCase().includes(want))
        out.push({ id: s.id, href: `/chat/${enc(s.id)}${h ? `#m${h.seq}` : ''}`, title: chats.title(s), snippet: h && plainSnippet(h.snippet), at: h?.at ?? s.last_message_at });
    }
    for (const h of first.values()) {
      if (out.length === 5) break;
      if (out.some((r) => r.id === h.session_id)) continue;
      const s = chats.list.find((x) => x.id === h.session_id);
      const snippet = plainSnippet(h.snippet);
      out.push({ id: h.session_id, href: `/chat/${enc(h.session_id)}#m${h.seq}`, title: (s && chats.title(s)) || h.title.trim(), snippet, at: h.at });
    }
    return out;
  });

  $effect(() => {
    const d = el;
    if (!d) return;
    if (shell.search && !d.open) {
      opener = document.activeElement instanceof HTMLElement ? document.activeElement : null;
      q = '';
      hits = null;
      active = 0;
      d.showModal();
      void tick().then(() => input?.focus());
    } else if (!shell.search && d.open) d.close();
  });

  $effect(() => {
    const t = term;
    const mine = ++seq;
    active = 0;
    hits = null;
    if (!t) return;
    const timer = setTimeout(async () => {
      let found: SearchHit[] = [];
      try {
        found = (await api.get<SearchHit[]>(`/api/search${qs({ q: t, limit: 20 })}`)) ?? [];
      } catch {
        // title matches from the chat list still show
      }
      if (mine !== seq) return;
      hits = found;
      await tick();
      announce(rows.length ? plural(rows.length, 'result') : `No results for “${t}”.`);
    }, 200);
    return () => clearTimeout(timer);
  });

  // Closes at once rather than on the next effect, so a key pressed right after sees no open dialog.
  function close() {
    shell.search = false;
    el?.close();
  }

  function go(r: Row | undefined) {
    if (!r) return;
    // The new page takes focus, so it doesn't go back to the opener.
    opener = null;
    close();
    router.navigate(r.href);
  }

  function key(e: KeyboardEvent) {
    const n = rows.length;
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      e.preventDefault();
      if (n) active = (active + (e.key === 'ArrowDown' ? 1 : -1) + n) % n;
      document.getElementById(`${uid}-o${active}`)?.scrollIntoView({ block: 'nearest' });
    } else if (e.key === 'Enter') {
      e.preventDefault();
      go(rows[active]);
    }
  }
</script>

{#snippet marked(text: string)}{#each highlight(text, term) as p, i (i)}{#if p.hit}<mark class="rounded-sm bg-warning-subtle text-text">{p.t}</mark>{:else}{p.t}{/if}{/each}{/snippet}

<!-- svelte-ignore a11y_no_noninteractive_element_interactions, a11y_click_events_have_key_events -->
<dialog
  bind:this={el}
  aria-label="Search"
  aria-modal="true"
  class="mx-auto mt-[15vh] mb-auto max-h-[70vh] w-[calc(100%-2rem)] max-w-[40rem] flex-col overflow-hidden rounded-lg border border-border bg-surface p-0 text-text shadow-[var(--shadow-overlay)] transition-[opacity,scale] duration-180 ease-out open:flex starting:open:scale-98 starting:open:opacity-0 max-sm:m-0 max-sm:h-dvh max-sm:max-h-none max-sm:w-full max-sm:max-w-none max-sm:rounded-none max-sm:border-0"
  oncancel={(e) => {
    e.preventDefault();
    close();
  }}
  onclose={() => {
    // A close event queued before a quick reopen must not close the new one.
    if (el?.open) return;
    shell.search = false;
    if (opener?.isConnected) opener.focus();
    opener = null;
  }}
  onkeydown={(e) => {
    if (e.key === 'Escape') {
      e.preventDefault();
      e.stopPropagation();
      close();
    } else if (el) trapTab(e, el);
  }}
  onclick={(e) => {
    if (e.target === el) close();
  }}
>
  {#if shell.search}
    <div class="flex h-13 shrink-0 items-center gap-3 border-b border-border px-4 focus-within:border-focus pointer-coarse:pr-2">
      <Icon name="search" class="text-muted" />
      <input
        bind:this={input}
        bind:value={q}
        type="text"
        role="combobox"
        aria-expanded={rows.length > 0}
        aria-controls="{uid}-list"
        aria-activedescendant={rows.length ? `${uid}-o${active}` : undefined}
        aria-autocomplete="list"
        aria-label="Search chats"
        placeholder="Search chats"
        autocomplete="off"
        spellcheck="false"
        class="h-full min-w-0 flex-1 bg-transparent text-text outline-none placeholder:text-faint sm:pointer-fine:text-reading"
        onkeydown={key}
      />
      <span class="hidden text-caption text-faint pointer-fine:inline" aria-hidden="true">Esc</span>
      <IconButton icon="close" label="Close" variant="quiet" class="pointer-fine:hidden" onclick={close} />
    </div>
    <div id="{uid}-list" role="listbox" aria-label={term ? 'Chats' : 'Recent chats'} class="min-h-0 flex-1 overflow-y-auto p-2">
      {#if rows.length}
        <p class="px-2 pt-1 pb-1 text-caption text-muted" aria-hidden="true">{term ? 'Chats' : 'Recent chats'}</p>
        {#each rows as r, i (r.id)}
          <div
            id="{uid}-o{i}"
            role="option"
            aria-selected={i === active}
            tabindex="-1"
            class="flex min-h-10 cursor-pointer flex-col justify-center rounded-md px-2 py-1.5 aria-selected:bg-hover pointer-coarse:min-h-12"
            onclick={() => go(r)}
            onpointermove={() => (active = i)}
          >
            <span class="flex items-baseline gap-3">
              <span class="min-w-0 flex-1 truncate text-body text-text">{@render marked(r.title)}</span>
              <span class="shrink-0 text-small text-muted">{dayLabel(r.at, clock.now)}</span>
            </span>
            {#if r.snippet}<span class="truncate text-small text-muted">{@render marked(r.snippet)}</span>{/if}
          </div>
        {/each}
      {:else if term && hits}
        <p class="px-2 py-2 text-body text-muted">No results for “{term}”.</p>
      {/if}
    </div>
  {/if}
</dialog>
