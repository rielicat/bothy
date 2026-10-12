<script lang="ts">
  import { renderMarkdown, type MdOptions } from '../lib/markdown';
  import { copyText } from '../lib/stores/ui.svelte';

  let { text, exposed = false, allow = [], class: cls = '' }: { text: string; class?: string } & MdOptions = $props();

  // renderMarkdown escapes everything and emits only a fixed tag set (see
  // lib/markdown.ts and its XSS corpus); this is the only {@html} in the app.
  const html = $derived(renderMarkdown(text ?? '', { exposed, allow }));

  function onclick(e: MouseEvent) {
    const b = (e.target as HTMLElement).closest('button.md-copy');
    if (!(b instanceof HTMLButtonElement)) return;
    const code = b.hasAttribute('data-copy-code') ? b.parentElement?.querySelector('code')?.textContent : null;
    void copyText(code ?? b.dataset.copy ?? '', code ? 'Code copied' : 'Link copied');
  }
</script>

<!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
<div class="md {cls}" {onclick}>{@html html}</div>
