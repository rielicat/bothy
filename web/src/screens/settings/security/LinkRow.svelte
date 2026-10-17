<script lang="ts">
  import Icon from '../../../components/Icon.svelte';
  import { router } from '../../../lib/router.svelte';

  // A settings row that opens a sub-page: "Allowed without asking   4 permissions ›" (§11.6).
  // `help` sits inside the row, above its hairline, so it reads as part of the row and not of what follows.
  let { label, value, href, tone = 'muted', help }: { label: string; value: string; href: string; tone?: 'muted' | 'danger'; help?: string } = $props();
</script>

<div class="border-b border-border last:border-b-0">
  <a
    {href}
    onclick={router.link}
    class="-mx-2 grid min-h-14 grid-cols-[minmax(0,1fr)_auto] items-center gap-x-6 rounded-md px-2 py-3 hover:bg-subtle focus-visible:outline-offset-[-2px] sm:grid-cols-[minmax(0,1fr)_minmax(0,20rem)]"
  >
    <span class="text-body font-medium text-text">{label}</span>
    <span class="inline-flex items-center gap-1 text-body {tone === 'danger' ? 'text-danger-text' : 'text-muted'}">{value}<Icon name="chevron-right" /></span>
  </a>
  {#if help}<p class="field-help -mt-2 pb-3">{help}</p>{/if}
</div>
