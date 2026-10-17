<script lang="ts">
  import type { Phrase } from '../../lib/activity';
  import Verbatim from '../Verbatim.svelte';

  let { phrase }: { phrase: Phrase } = $props();
</script>

<!-- Values are verbatim: what Bothy touched, never paraphrased, so the copy check skips them. -->
<!-- A repository reads like a citation: "acme/web · src/auth/callback.ts". -->
{#each phrase as s, i (i)}{#if s.k === 'text'}{s.v}{:else if s.k === 'code'}<code class="font-mono text-code [overflow-wrap:anywhere]" data-copy-ignore
      ><Verbatim text={s.v} /></code
    >{:else if s.k === 'repo'}<span data-copy-ignore><Verbatim text={s.v} /></span>{' · '}{:else}<span data-copy-ignore><Verbatim text={s.v} /></span>{/if}{/each}
