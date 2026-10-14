<script lang="ts" module>
  export interface Bar {
    /** Axis label on wide screens; '' leaves the slot empty. */
    label: string;
    /** Axis label under 640 px. */
    short?: string;
    value: number;
    /** What the bar's tooltip and the hidden table say: "Tue 14 Oct: $1.20". */
    title: string;
  }
</script>

<script lang="ts">
  import { tooltip } from './ui';

  // Neutral bars, two labelled gridlines, the hovered or focused bar in ink;
  // the summary and a visually hidden table carry the numbers (§10.5, §16).
  let {
    data,
    format,
    axis = format,
    summary,
    height = 160,
  }: { data: Bar[]; format: (v: number) => string; axis?: (v: number) => string; summary: string; height?: number } = $props();

  const max = $derived(Math.max(0, ...data.map((d) => d.value)));
  // The top gridline lands on 1, 2, 4 or 5 × 10^n, so both labels read cleanly.
  const top = $derived.by(() => {
    if (max <= 0) return 1;
    const p = 10 ** Math.floor(Math.log10(max));
    return ([1, 2, 4, 5, 10].find((m) => m * p >= max) ?? 10) * p;
  });
  const few = $derived(data.length <= 7);
</script>

<figure class="mt-6">
  <div role="group" aria-label={summary} class="relative pl-12" style:height="{height}px">
    {#each [0.5, 1] as g (g)}
      <div class="absolute right-0 left-12 border-t border-border" style:bottom="{g * 100}%" aria-hidden="true">
        <span class="absolute -top-2 -left-12 w-10 text-right text-caption text-muted tabular-nums">{axis(top * g)}</span>
      </div>
    {/each}
    <div class="absolute inset-y-0 right-0 left-12 flex items-end border-b border-border {data.length > 14 ? 'gap-px' : 'gap-1'}">
      {#each data as d, i (i)}
        <div class="flex h-full min-w-0 flex-1 flex-col items-center justify-end">
          {#if few && d.value > 0}<span class="mb-1 text-caption text-muted tabular-nums" aria-hidden="true">{format(d.value)}</span>{/if}
          <!-- Bars take focus so the keyboard reaches each day's tooltip (§10.5). -->
          <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
          <span
            role="img"
            tabindex="0"
            aria-label={d.title}
            class="block w-full max-w-12 bg-chart outline-offset-2 hover:bg-text focus-visible:bg-text"
            style:height="{Math.max(d.value > 0 ? 2 : 0, (d.value / top) * 100)}%"
            {@attach tooltip(() => d.title)}
          ></span>
        </div>
      {/each}
    </div>
  </div>
  <div class="mt-1 flex pl-12 text-caption text-muted {data.length > 14 ? 'gap-px' : 'gap-1'}" aria-hidden="true">
    {#each data as d, i (i)}
      {@const edge = i === 0 ? 'justify-start' : i === data.length - 1 ? 'justify-end' : 'justify-center'}
      <span class="flex min-w-0 flex-1 whitespace-nowrap max-sm:hidden {edge}">{d.label}</span>
      <span class="flex min-w-0 flex-1 whitespace-nowrap sm:hidden {edge}">{d.short ?? d.label}</span>
    {/each}
  </div>
  <!-- sr-only on the wrapper: a table ignores the 1 px width and would widen the page. -->
  <div class="sr-only">
    <table>
      <caption>{summary}</caption>
      <thead><tr><th scope="col">Day</th><th scope="col">Spent</th></tr></thead>
      <tbody>
        {#each data as d, i (i)}<tr><td>{d.title.split(':')[0]}</td><td>{format(d.value)}</td></tr>{/each}
      </tbody>
    </table>
  </div>
</figure>
