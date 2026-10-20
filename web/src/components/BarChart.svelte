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

  /** Two gridlines at round amounts (1, 2 or 5 × 10ⁿ) at or under the highest bar: 2 and 4 for a high of 4.10. */
  export function gridlines(max: number): number[] {
    if (max <= 0) return [];
    const half = max / 2;
    const p = 10 ** Math.floor(Math.log10(half));
    const step = ([5, 2, 1].find((m) => m * p <= half) ?? 1) * p;
    return [step, step * 2];
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
    class: cls = '',
  }: { data: Bar[]; format: (v: number) => string; axis?: (v: number) => string; summary: string; height?: number; class?: string } = $props();

  const max = $derived(Math.max(0, ...data.map((d) => d.value)));
  const few = $derived(data.length <= 7);
  const lines = $derived(gridlines(max));
  // Headroom for the amounts printed above the bars when there are few of them.
  const top = $derived(max > 0 ? max * (few ? 1.15 : 1) : 1);
</script>

<figure class={cls}>
  <div role="group" aria-label={summary} class="relative pl-12" style:height="{height}px">
    {#each lines as g (g)}
      <div class="absolute right-0 left-12 border-t border-border" style:bottom="{(g / top) * 100}%" aria-hidden="true">
        <span class="absolute -top-2 -left-12 w-10 text-right text-caption text-muted tabular-nums">{axis(g)}</span>
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
