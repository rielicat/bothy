<script lang="ts">
  import { renderMarkdown, type MdOptions } from '../lib/markdown';
  import { copyText } from '../lib/stores/ui.svelte';

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
    void copyText(code ?? b.dataset.copy ?? '', code ? 'Code copied' : 'Link copied');
  }
</script>

<!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
<div class="md {cls}" {onclick}>{@html html}</div>
