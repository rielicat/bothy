<script lang="ts">
  import Verbatim from './Verbatim.svelte';

  let { diff }: { diff: string } = $props();
  const lines = $derived(diff.replace(/\n$/, '').split('\n'));
  const tone = (l: string) =>
    l.startsWith('+++') || l.startsWith('---')
      ? 'text-muted'
      : l.startsWith('+')
        ? 'bg-ok-soft text-ok'
        : l.startsWith('-')
          ? 'bg-bad-soft text-bad'
          : l.startsWith('@@')
            ? 'text-info'
            : '';
</script>

<pre class="mono max-h-96 overflow-auto rounded-lg border border-line bg-sunken py-2 leading-5" aria-label="Diff"><code
    >{#each lines as l, i (i)}<span class="block px-3 whitespace-pre {tone(l)}"><Verbatim text={l || ' '} /></span>{/each}</code
  ></pre>
