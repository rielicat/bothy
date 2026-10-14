<script lang="ts">
  let {
    options,
    value = $bindable(),
    label,
    onchange,
    class: cls = '',
  }: { options: { value: string; label: string }[]; value?: string; label: string; onchange?: (value: string) => void; class?: string } = $props();

  let buttons: HTMLButtonElement[] = $state([]);
  const selected = $derived(options.findIndex((o) => o.value === value));

  function select(i: number, focus = false) {
    const o = options[i];
    if (!o) return;
    if (o.value !== value) {
      value = o.value;
      onchange?.(o.value);
    }
    if (focus) buttons[i]?.focus();
  }

  function key(e: KeyboardEvent, i: number) {
    const n = options.length;
    const next = { ArrowRight: i + 1, ArrowDown: i + 1, ArrowLeft: i - 1, ArrowUp: i - 1, Home: 0, End: n - 1 }[e.key];
    if (next === undefined) return;
    e.preventDefault();
    select((next + n) % n, true);
  }
</script>

<!-- A radio group: arrow keys move the choice, one tab stop for the whole control. -->
<div role="radiogroup" aria-label={label} class="inline-flex h-9 items-stretch gap-0.5 rounded-md border border-border p-0.5 pointer-coarse:h-auto {cls}">
  {#each options as o, i (o.value)}
    <button
      bind:this={buttons[i]}
      type="button"
      role="radio"
      aria-checked={o.value === value}
      tabindex={i === (selected < 0 ? 0 : selected) ? 0 : -1}
      class="inline-flex cursor-pointer items-center rounded-sm px-3 text-small font-medium whitespace-nowrap text-muted transition-colors duration-120 hover:text-text aria-checked:bg-selected aria-checked:text-text focus-visible:outline-offset-0 pointer-coarse:min-h-11"
      onclick={() => select(i)}
      onkeydown={(e) => key(e, i)}>{o.label}</button
    >
  {/each}
</div>
