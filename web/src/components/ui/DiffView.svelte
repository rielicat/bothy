<script lang="ts">
  import { plural } from '../../lib/format';
  import Verbatim from '../Verbatim.svelte';
  import CodeBlock from './CodeBlock.svelte';
  import Disclosure from './Disclosure.svelte';
  import { fileLines, parseDiff, proseBlocks, type DiffLine, type DiffRow } from './diff';
  import LinkButton from './LinkButton.svelte';

  let {
    diff,
    mode = 'code',
    path,
    maxLines = 200,
    class: cls = '',
  }: {
    /** A unified diff; for new-file and deleted-file modes the plain file content works too. */
    diff: string;
    mode?: 'code' | 'new-file' | 'deleted-file' | 'prose';
    path?: string;
    maxLines?: number;
    class?: string;
  } = $props();

  const parsed = $derived.by(() => {
    if (mode === 'new-file') {
      const lines = fileLines(diff);
      return { path: path ?? parseDiff(diff).path, added: lines.length, removed: 0, rows: lines.map((text, i): DiffRow => ({ kind: 'add', text, no: i + 1 })) };
    }
    const p = parseDiff(diff);
    return { ...p, path: path ?? p.path };
  });

  let all = $state(false);
  const lineCount = $derived(parsed.rows.filter((r) => r.kind !== 'hunk').length);
  const shownRows = $derived.by(() => {
    if (all || lineCount <= maxLines) return parsed.rows;
    const out: DiffRow[] = [];
    let n = 0;
    for (const r of parsed.rows) {
      if (r.kind !== 'hunk' && ++n > maxLines) break;
      out.push(r);
    }
    return out;
  });

  const deleted = $derived(mode === 'deleted-file' ? fileLines(diff) : []);
  const prose = $derived(mode === 'prose' ? proseBlocks(diff) : []);

  const rowBg = { add: 'bg-diff-add', del: 'bg-danger-subtle', ctx: '' };
  const glyph = { add: '+', del: '−', ctx: ' ' };
  const glyphColor = { add: 'text-accent-text', del: 'text-danger-text', ctx: '' };
  const asLine = (r: DiffRow) => r as DiffLine;
</script>

{#snippet seg(kind: 'same' | 'add' | 'del', text: string)}{#if kind === 'same'}{text}{:else if kind === 'del'}<del class="bg-danger-subtle decoration-danger-text"
      ><span class="sr-only">{'Removed: '}</span>{text}</del
    >{:else}<ins class="bg-diff-add decoration-accent-text underline-offset-2"><span class="sr-only">{'Added: '}</span>{text}</ins>{/if}{/snippet}

{#if mode === 'deleted-file'}
  <div class="min-w-0 {cls}">
    <p class="text-body text-text">The whole file will be removed ({plural(deleted.length, 'line')}).</p>
    <Disclosure label="Show content" class="mt-1">
      <CodeBlock code={deleted.join('\n')} lineNumbers maxLines={maxLines} />
    </Disclosure>
  </div>
{:else if mode === 'prose'}
  <!-- Deletions are struck through and insertions underlined, so the change never rests on colour alone. -->
  <div class="min-w-0 space-y-1 text-reading text-text {cls}" data-copy-ignore>
    {#each prose as b, i (i)}
      {#if b.kind === 'gap'}
        <p class="min-h-6 text-muted" aria-label="Unchanged lines left out">…</p>
      {:else}
        <p class="min-h-6 break-words whitespace-pre-wrap {b.mark === 'heading' ? 'font-medium' : ''}">{#if b.mark === 'bullet'}<span class="text-muted" aria-hidden="true">•{' '}</span>{/if}{#if b.kind === 'change'}{#each b.segments as s, j (j)}{@render seg(
                s.kind,
                s.text,
              )}{/each}{:else}{@render seg(b.kind, b.text)}{/if}</p>
      {/if}
    {/each}
  </div>
{:else}
  <div class="min-w-0 overflow-hidden rounded-md bg-subtle {cls}">
    {#if parsed.path || parsed.added || parsed.removed}
      <div class="flex items-center gap-3 border-b border-border px-3 py-2 text-small">
        {#if parsed.path}<span class="min-w-0 truncate font-mono text-text" title={parsed.path} data-copy-ignore><Verbatim text={parsed.path} /></span>{/if}
        <span class="ml-auto shrink-0 tabular-nums">
          <span class="text-accent-text">+{parsed.added}</span>
          {#if mode !== 'new-file'}<span class="ml-1 text-danger-text">−{parsed.removed}</span>{/if}
        </span>
      </div>
    {/if}
    <!-- Wide lines scroll inside the box, never the page; focusable so the keyboard can scroll it. -->
    <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
    <div class="overflow-x-auto py-1" tabindex="0" role="group" aria-label={parsed.path ? `Changes to ${parsed.path}` : 'Changes'} data-copy-ignore>
      <div class="w-max min-w-full font-mono text-code text-text">
        {#each shownRows as r, i (i)}
          {#if r.kind === 'hunk'}
            <div class="px-3 pt-2 pb-1 font-sans text-caption text-muted">{r.label}</div>
          {:else}
            {@const l = asLine(r)}
            <div class="flex {rowBg[l.kind]}">
              <span class="w-12 shrink-0 pr-3 text-right text-muted tabular-nums select-none" aria-hidden="true">{l.no}</span>
              <span class="w-4 shrink-0 select-none {glyphColor[l.kind]}">{glyph[l.kind]}</span>
              <span class="pr-4 whitespace-pre"><Verbatim text={l.text || ' '} /></span>
            </div>
          {/if}
        {/each}
      </div>
    </div>
  </div>
  {#if !all && lineCount > maxLines}
    <LinkButton class="mt-1 text-small" onclick={() => (all = true)}>Show all {lineCount.toLocaleString()} lines</LinkButton>
  {/if}
{/if}
