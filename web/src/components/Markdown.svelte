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
  /* An inert link in an exposed chat reads as the host in muted text plus Copy (§7.3). */
  .md :global(.md-inert) {
    background: none;
    padding: 0;
    font-family: inherit;
    font-size: inherit;
    color: var(--color-muted);
  }
</style>
