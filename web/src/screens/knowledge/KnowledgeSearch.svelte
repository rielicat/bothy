<script lang="ts">
  import Button from '../../components/ui/Button.svelte';
  import CodeBlock from '../../components/ui/CodeBlock.svelte';
  import Disclosure from '../../components/ui/Disclosure.svelte';
  import EmptyState from '../../components/ui/EmptyState.svelte';
  import PageHeader from '../../components/ui/PageHeader.svelte';
  import Segmented from '../../components/ui/Segmented.svelte';
  import Select from '../../components/ui/Select.svelte';
  import {
    githubUrl,
    knowledge,
    linesLabel,
    marks,
    plainText,
    projectHref,
    queryWords,
    readRepos,
    type KnowledgeHit,
    type KnowledgeRepo,
  } from '../../lib/api/knowledge';
  import { router } from '../../lib/router.svelte';
  import Loading from './Loading.svelte';
  import SearchField from './SearchField.svelte';

  const MAX_LINES = 6;

  const q = $derived(router.query.get('q')?.trim() ?? '');
  const type = $derived(['code', 'notes'].includes(router.query.get('type') ?? '') ? (router.query.get('type') as 'code' | 'notes') : 'all');
  const repoName = $derived(router.query.get('repo') ?? '');
  const words = $derived(queryWords(q));

  let text = $state('');
  let repos = $state<KnowledgeRepo[] | null>(null);
  let hits = $state<KnowledgeHit[] | null>(null);
  let failed = $state(false);
  let limit = $state(20);

  $effect(() => {
    text = q;
  });

  $effect(() => {
    void readRepos().then(
      (r) => (repos = r),
      () => (repos = []),
    );
  });

  let seq = 0;
  async function search() {
    const n = ++seq;
    failed = false;
    hits = null;
    const repo = repos?.find((r) => r.repo.toLowerCase() === repoName.toLowerCase())?.id;
    try {
      const found = await knowledge.search(q, { repo, limit });
      if (n === seq) hits = found;
    } catch {
      if (n === seq) failed = true;
    }
  }

  $effect(() => {
    void [q, repoName, limit];
    if (q && repos) void search();
    else hits = null;
  });

  const shown = $derived((hits ?? []).filter((h) => type === 'all' || (h.source ?? 'code') === type));

  // Code hits grouped per file, in rank order of each file's best hit; notes hits stay one per section.
  type Group = { key: string; repo: string; path: string; sha: string; notes: boolean; hits: KnowledgeHit[] };
  const groups = $derived.by(() => {
    const out = new Map<string, Group>();
    for (const h of shown) {
      const notes = h.source === 'notes';
      const key = notes ? `${h.repo}#notes#${h.title}` : `${h.repo}#${h.path}`;
      const g = out.get(key) ?? { key, repo: h.repo, path: h.path, sha: h.sha, notes, hits: [] };
      g.hits.push(h);
      out.set(key, g);
    }
    return [...out.values()];
  });

  const repoOptions = $derived([{ value: '', label: 'All repositories' }, ...(repos ?? []).map((r) => ({ value: r.repo, label: r.repo }))]);

  const set = (p: Record<string, string | null>, replace = true) => router.setQuery(p, { replace });

  /** Up to MAX_LINES lines around the first match, with their line numbers. */
  function excerpt(h: KnowledgeHit): { no: number; text: string }[] {
    const lines = h.snippet.replace(/\n$/, '').split('\n');
    const first = Math.max(0, lines.findIndex((l) => words.some((w) => l.toLowerCase().includes(w))));
    const start = Math.max(0, Math.min(first - 1, lines.length - MAX_LINES));
    return lines.slice(start, start + MAX_LINES).map((t, i) => ({ no: h.start_line + start + i, text: t }));
  }

  const ask = $derived(`/chat?prompt=${encodeURIComponent(q)}`);
</script>

<svelte:head><title>{q ? `${q} · ` : ''}Search · Bothy</title></svelte:head>

{#snippet marked(t: string)}{#each marks(t, words) as m, i (i)}{#if m.hit}<mark class="rounded-sm bg-warning-subtle text-text">{m.text}</mark>{:else}{m.text}{/if}{/each}{/snippet}

<div class="page">
  <PageHeader title="Search" back={{ href: '/knowledge', label: 'Knowledge' }} />
  <SearchField class="mt-6" clearable bind:value={text} onsearch={(t) => set({ q: t || null }, false)} />

  <div class="mt-4 flex flex-wrap items-center justify-between gap-x-6 gap-y-3">
    <!-- Short labels ("All") would make segments narrower than a finger on touch screens. -->
    <Segmented
      class="pointer-coarse:[&>button]:min-w-11 pointer-coarse:[&>button]:justify-center"
      label="Show"
      options={[
        { value: 'all', label: 'All' },
        { value: 'code', label: 'Code' },
        { value: 'notes', label: 'Notes' },
      ]}
      value={type}
      onchange={(v) => set({ type: v === 'all' ? null : v })}
    />
    <div class="flex min-w-0 items-center gap-2 max-sm:w-full">
      <label for="search-repo" class="text-body text-muted">In</label>
      <Select id="search-repo" class="min-w-0 flex-1 sm:w-56" options={repoOptions} value={repoName} onchange={(e) => set({ repo: e.currentTarget.value || null })} />
    </div>
  </div>

  <div class="mt-6">
    {#if !q}
      <!-- nothing asked yet -->
    {:else if !hits}
      <Loading {failed} onretry={search} text="The search didn't finish. Check your connection and try again." />
    {:else if !groups.length}
      <EmptyState text="Nothing in your projects or notes matches “{q}”." />
    {:else}
      <ul class="space-y-6" aria-label="Results">
        {#each groups as g (g.key)}
          {@const h0 = g.hits[0]}
          <li>
            {#if g.notes}
              <p class="text-body text-text">
                {g.repo} · Project notes · <a class="link" href={projectHref(g.repo)} onclick={router.link}>{h0.title}</a>
              </p>
              <p class="mt-1 line-clamp-3 text-reading text-text" data-copy-ignore>{@render marked(plainText(h0.snippet))}</p>
            {:else}
              <div class="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
                <p class="min-w-0 text-body text-text [overflow-wrap:anywhere]">
                  {g.repo} · <span class="font-mono text-code" data-path data-copy-ignore>{g.path}</span>, {g.hits.map((h) => linesLabel(h.start_line, h.end_line)).join(', ').replace(/, lines /g, ', ')}
                </p>
                <a
                  class="link -my-0.5 inline-flex min-h-6 shrink-0 items-center text-small pointer-coarse:-my-3 pointer-coarse:min-h-11"
                  href={githubUrl(g.repo, { sha: g.sha, path: g.path, start: h0.start_line, end: h0.end_line })}
                  target="_blank"
                  rel="noopener noreferrer">Open on GitHub ↗<span class="sr-only">{' (opens in a new tab)'}</span></a
                >
              </div>
              {#each g.hits as h (h.citation)}
                <!-- Wide lines scroll inside the block, never the page; focusable so the keyboard can scroll it. -->
                <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
                <pre class="mt-2 overflow-x-auto rounded-md bg-subtle py-2 text-code text-text" tabindex="0" aria-label="{g.path}, {linesLabel(h.start_line, h.end_line)}" data-copy-ignore><code
                    >{#each excerpt(h) as l (l.no)}<span class="flex"
                        ><span class="w-12 shrink-0 pr-3 text-right text-faint tabular-nums select-none" aria-hidden="true">{l.no}</span><span class="pr-4 whitespace-pre"
                          >{@render marked(l.text || ' ')}</span
                        ></span
                      >{/each}</code
                  ></pre>
                <Disclosure class="mt-1">
                  <CodeBlock code={h.citation} wrap label="Citation" />
                </Disclosure>
              {/each}
            {/if}
          </li>
        {/each}
      </ul>
      {#if hits.length >= limit && limit < 50}
        <Button class="mt-6" variant="ghost" onclick={() => (limit = 50)}>Show more</Button>
      {/if}
    {/if}
    {#if q && hits}
      <Button class="mt-6" href={ask}>Ask Bothy about “{q}”</Button>
    {/if}
  </div>
</div>
