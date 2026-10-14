<script lang="ts">
  let {
    value,
    max = 100,
    tone = 'default',
    valueText,
    label,
    class: cls = '',
  }: {
    value: number;
    max?: number;
    tone?: 'default' | 'warning' | 'danger';
    /** What a screen reader hears, in money: "$4.10 of $5.00". */
    valueText: string;
    label: string;
    class?: string;
  } = $props();

  const pct = $derived(max > 0 ? Math.min(100, Math.max(0, (value / max) * 100)) : 0);
  const fill = { default: 'bg-accent', warning: 'bg-warning-text', danger: 'bg-danger' };
</script>

<div
  role="progressbar"
  aria-label={label}
  aria-valuemin={0}
  aria-valuemax={max}
  aria-valuenow={Math.min(value, max)}
  aria-valuetext={valueText}
  class="h-1 w-full overflow-hidden rounded-full bg-border {cls}"
>
  <div class="h-full rounded-full {fill[tone]}" style:width="{pct}%"></div>
</div>
