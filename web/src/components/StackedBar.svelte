<script module lang="ts">
  import type { Breakdown } from '../lib/api/types.gen';

  export const regions: { key: keyof Breakdown; label: string; cls: string }[] = [
    { key: 'constitution', label: 'Constitution', cls: 'bg-r1' },
    { key: 'persona', label: 'Persona', cls: 'bg-r6' },
    { key: 'memory', label: 'Memory', cls: 'bg-r3' },
    { key: 'skills', label: 'Skills', cls: 'bg-r4' },
    { key: 'pins', label: 'Pins', cls: 'bg-r5' },
    { key: 'tools', label: 'Tools', cls: 'bg-r2' },
    { key: 'summary', label: 'Summary', cls: 'bg-faint' },
    { key: 'history', label: 'History', cls: 'bg-accent' },
    { key: 'tool_results', label: 'Tool results', cls: 'bg-info' },
    { key: 'turn_context', label: 'Turn context', cls: 'bg-warn' },
  ];
</script>

<script lang="ts">
  import { tokens } from '../lib/format';

  let { b, marker, legend = false }: { b: Breakdown; marker?: number; legend?: boolean } = $props();

  const win = $derived(Math.max(b.window || b.total + b.reserve, 1));
  const pct = (n: number) => `${Math.max(0, (n / win) * 100)}%`;
</script>

<div>
  <div
    class="relative flex h-3 w-full overflow-hidden rounded-full bg-sunken"
    role="img"
    aria-label="{tokens(b.total)} of {tokens(b.window)} tokens used, {tokens(b.reserve)} reserved for the reply"
  >
    {#each regions as r (r.key)}
      {#if (b[r.key] as number) > 0}<span class="h-full {r.cls}" style:width={pct(b[r.key] as number)} title="{r.label}: {tokens(b[r.key] as number)}"></span>{/if}
    {/each}
    <span class="ml-auto h-full bg-line-strong/70" style:width={pct(b.reserve)} title="Reserved for the reply: {tokens(b.reserve)}"></span>
    {#if marker}
      <span class="absolute inset-y-0 w-0.5 bg-fg/70" style:left="{marker * 100}%" title="Compaction starts around here"></span>
    {/if}
  </div>
  {#if legend}
    <ul class="mt-3 grid grid-cols-2 gap-x-4 gap-y-1.5 text-xs sm:grid-cols-3">
      {#each regions as r (r.key)}
        <li class="flex items-center gap-2">
          <span class="size-2.5 rounded-sm {r.cls}"></span><span class="flex-1 text-muted">{r.label}</span><span class="tabular-nums">{tokens(b[r.key] as number)}</span>
        </li>
      {/each}
      <li class="flex items-center gap-2">
        <span class="size-2.5 rounded-sm bg-line-strong"></span><span class="flex-1 text-muted">Reply reserve</span><span class="tabular-nums">{tokens(b.reserve)}</span>
      </li>
    </ul>
  {/if}
</div>
