<script lang="ts">
  import type { Seg } from '../../lib/cardcopy';
  import Verbatim from '../Verbatim.svelte';

  // Copy with identifiers in it: paths, hosts and commands show in mono. Every
  // part goes through Verbatim, since a plain phrase can still quote one.
  let { segs }: { segs: Seg[] } = $props();
</script>

{#each segs as s, i (i)}{#if typeof s === 'string'}<Verbatim text={s} />{:else}<span class="font-mono text-code" data-copy-ignore><Verbatim text={s.code} /></span>{/if}{/each}
