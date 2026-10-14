<script lang="ts">
  import type { Breakdown, CallInfo } from '../../lib/api/types.gen';
  import { money } from '../../lib/format';
  import CodeBlock from '../ui/CodeBlock.svelte';

  // Chat details → Technical details (§7.7): the only place in the chat where
  // context size, tokens and model ids appear.
  let { breakdown, calls, chatId }: { breakdown: Breakdown | null; calls: CallInfo[]; chatId: string } = $props();

  const n = (v: number) => v.toLocaleString();
  const secs = (ns: number) => `${(ns / 1e9).toFixed(1)} s`;
  const parts = $derived(
    breakdown
      ? ([
          ['Built-in rules', breakdown.constitution],
          ['Personality', breakdown.persona],
          ['Memory', breakdown.memory],
          ['Skills', breakdown.skills],
          ['Pinned', breakdown.pins],
          ['Tools', breakdown.tools],
          ['Summary', breakdown.summary],
          ['History', breakdown.history],
          ['Tool results', breakdown.tool_results],
          ['Turn context', breakdown.turn_context],
          ['Reply reserve', breakdown.reserve],
        ] as const)
      : [],
  );
  const last = $derived(calls[calls.length - 1]);
  const cache = $derived.by(() => {
    const u = last?.usage;
    const all = u ? u.input + u.cache_read + u.cache_write : 0;
    return all ? Math.round((u!.cache_read / all) * 100) : null;
  });
</script>

<div class="space-y-4 text-small text-text" data-details>
  {#if breakdown}
    <p>Context: {n(breakdown.total)} of {n(breakdown.window)} tokens ({Math.round((breakdown.total / Math.max(1, breakdown.window)) * 100)}%)</p>
    <table class="w-full text-small">
      <thead><tr class="border-b border-border text-left text-muted"><th class="py-1 font-medium">Part</th><th class="py-1 text-right font-medium">Tokens</th></tr></thead>
      <tbody>
        {#each parts as [label, v] (label)}
          <tr class="border-b border-border last:border-b-0"><td class="py-1">{label}</td><td class="py-1 text-right tabular-nums">{n(v)}</td></tr>
        {/each}
      </tbody>
    </table>
  {/if}
  {#if last}
    <p>{#if cache !== null}Cache hits {cache}%{' · '}{/if}Time to first word {secs(last.ttft)}</p>
  {/if}
  {#if calls.length}
    <div>
      <p class="mb-1 text-muted">Per-step calls</p>
      <ul class="space-y-1">
        {#each calls as c (c.id)}
          <li class="[overflow-wrap:anywhere]">
            Step {c.step} · <code class="font-mono">{c.ref.provider}/{c.ref.model}</code> · {c.attempts?.length || 1}
            {(c.attempts?.length || 1) === 1 ? 'attempt' : 'attempts'} · first word {secs(c.ttft)} · {money(c.cost_micros, { known: c.price_known })}
          </li>
        {/each}
      </ul>
    </div>
  {/if}
  <div>
    <p class="mb-1 text-muted">Chat id</p>
    <CodeBlock code={chatId} />
  </div>
</div>
