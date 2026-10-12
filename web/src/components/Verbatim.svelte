<script lang="ts">
  import { punycode, visualize } from '../lib/text';

  let { text }: { text: string } = $props();
  const segs = $derived(visualize(text ?? '').map((s) => (s.hidden ? s : { text: punycode(s.text), hidden: false })));
</script>

{#each segs as s, i (i)}{#if s.hidden}<span
      class="mx-px rounded bg-bad-soft px-0.5 font-mono text-[0.8em] text-bad"
      title="Invisible character {s.text}">⟨{s.text}⟩</span
    >{:else}{s.text}{/if}{/each}
