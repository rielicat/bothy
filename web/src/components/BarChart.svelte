<script lang="ts">
  let {
    data,
    label,
    format = (v: number) => String(v),
  }: { data: { key: string; label: string; value: number; muted?: boolean }[]; label: string; format?: (v: number) => string } = $props();

  const W = 760;
  const H = 170;
  const pad = { l: 44, r: 8, t: 10, b: 24 };
  const max = $derived(Math.max(...data.map((d) => d.value), 0));
  // Round the axis top to 1/2/5 × 10^n so gridlines land on readable values.
  const top = $derived.by(() => {
    if (max <= 0) return 1;
    const p = 10 ** Math.floor(Math.log10(max));
    return ([1, 2, 5, 10].find((m) => m * p >= max) ?? 10) * p;
  });
  const bw = $derived((W - pad.l - pad.r) / Math.max(1, data.length));
  const y = (v: number) => pad.t + (H - pad.t - pad.b) * (1 - v / top);
  const every = $derived(Math.ceil(data.length / 8));
</script>

<svg viewBox="0 0 {W} {H}" class="h-auto w-full" role="img" aria-label="{label}. Highest: {format(max)}">
  {#each [0, 0.5, 1] as g (g)}
    <line x1={pad.l} x2={W - pad.r} y1={y(top * g)} y2={y(top * g)} class="stroke-line" stroke-dasharray={g ? '3 4' : ''} />
    <text x={pad.l - 6} y={y(top * g) + 4} text-anchor="end" class="fill-faint text-[10px]">{format(top * g)}</text>
  {/each}
  {#each data as d, i (d.key)}
    {@const h = Math.max(d.value > 0 ? 2 : 0, y(0) - y(d.value))}
    <rect
      x={pad.l + i * bw + bw * 0.15}
      y={y(0) - h}
      width={Math.max(1, bw * 0.7)}
      height={h}
      rx="3"
      class={d.muted ? 'fill-warn' : 'fill-accent'}
      opacity={d.value > 0 ? 0.9 : 0}
    >
      <title>{d.label}: {format(d.value)}{d.muted ? ' (includes unknown prices)' : ''}</title>
    </rect>
    {#if i % every === 0}
      <text x={pad.l + i * bw + bw / 2} y={H - 6} text-anchor="middle" class="fill-faint text-[10px]">{d.label}</text>
    {/if}
  {/each}
</svg>
