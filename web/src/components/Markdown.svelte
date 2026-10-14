<script lang="ts">
  import { renderMarkdown, type MdOptions } from '../lib/markdown';
  import { toast } from './ui/toast.svelte';

  let { text, exposed = false, allow = [], class: cls = '' }: { text: string; class?: string } & MdOptions = $props();

  // The transcript rebuilds its row objects on every streamed delta, which
  // re-runs this derived for every message; render again only when the
  // inputs actually changed.
  let memo = { text: '', exposed: false, allow: '', html: '' };

  // renderMarkdown escapes everything and emits only a fixed tag set (see
  // lib/markdown.ts and its XSS corpus); this is the only {@html} in the app.
  const html = $derived.by(() => {
    const t = text ?? '';
    const a = allow.join(',');
    if (t !== memo.text || exposed !== memo.exposed || a !== memo.allow) memo = { text: t, exposed, allow: a, html: renderMarkdown(t, { exposed, allow }) };
    return memo.html;
  });

  function onclick(e: MouseEvent) {
    const b = (e.target as HTMLElement).closest('button.md-copy');
    if (!(b instanceof HTMLButtonElement)) return;
    const code = b.hasAttribute('data-copy-code') ? b.parentElement?.querySelector('code')?.textContent : null;
    navigator.clipboard.writeText(code ?? b.dataset.copy ?? '').then(
      () => toast('Copied.'),
      () => {},
    );
  }
</script>

<!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
<div class="md {cls}" {onclick}>{@html html}</div>

<style>
  /* §7.3 rules on top of the shared .md base in app.css. */
  /* An inert link in an exposed chat reads as the host in muted text plus Copy. */
  .md :global(.md-inert) {
    background: none;
    padding: 0;
    font-family: inherit;
    font-size: inherit;
    color: var(--color-muted);
  }
  .md :global(:is(h1, h2) + *) {
    margin-top: 0.5rem;
  }
  .md :global(hr) {
    margin-block: 1.5rem;
  }
  .md :global(table) {
    font-size: var(--text-small);
    line-height: var(--text-small--line-height);
  }
  .md :global(th) {
    color: var(--color-muted);
  }
  /* A code block is a tint with no border inside it; Copy sits on the same tint so it hides the code under it. */
  .md :global(pre .md-copy) {
    top: 0;
    right: 0;
    border-color: transparent;
    border-radius: 0 var(--radius-md) 0 var(--radius-sm);
    padding: 0.375rem 0.75rem;
    background: var(--color-subtle);
  }
  .md :global(pre .md-copy:hover) {
    background: var(--color-hover);
  }
  @media (hover: hover) and (pointer: fine) {
    .md :global(pre:not(:hover, :focus-within) .md-copy) {
      opacity: 0;
    }
  }
</style>
