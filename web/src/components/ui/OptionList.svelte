<script lang="ts" module>
  export interface Option {
    value: string;
    title: string;
    description?: string;
    disabled?: boolean;
    /** Why the option can't be chosen, shown in its row. */
    reason?: string;
    /** False when `expanded` has nothing for this option, so no empty area opens under it. */
    expandable?: boolean;
  }
</script>

<script lang="ts">
  import type { Snippet } from 'svelte';

  let {
    name,
    options,
    value = $bindable(),
    expanded,
    framed = true,
    label,
    onchange,
    class: cls = '',
  }: {
    name: string;
    options: Option[];
    value?: string;
    /** Extra controls for the chosen option, rendered inside its row. */
    expanded?: Snippet<[string]>;
    /** One card around the rows; off inside dialogs. */
    framed?: boolean;
    /** Names the group for screen readers when no visible heading does. */
    label?: string;
    onchange?: (value: string) => void;
    class?: string;
  } = $props();

  const uid = $props.id();
  let inputs: HTMLInputElement[] = $state([]);

  function choose(v: string) {
    if (v === value) return;
    value = v;
    onchange?.(v);
  }

  // Native radio groups already move with the arrow keys; doing it here keeps
  // the behaviour identical everywhere and skips disabled options explicitly.
  function key(e: KeyboardEvent, i: number) {
    const step = e.key === 'ArrowDown' || e.key === 'ArrowRight' ? 1 : e.key === 'ArrowUp' || e.key === 'ArrowLeft' ? -1 : 0;
    if (!step) return;
    e.preventDefault();
    const n = options.length;
    for (let k = 1; k <= n; k++) {
      const j = (((i + step * k) % n) + n) % n;
      if (!options[j].disabled) {
        choose(options[j].value);
        inputs[j]?.focus();
        return;
      }
    }
  }
</script>

<div role="radiogroup" aria-label={label} class="{framed ? 'card p-0' : ''} {cls}">
  {#each options as o, i (o.value)}
    {@const selected = o.value === value}
    {@const described = !!(o.description || (o.disabled && o.reason))}
    <div class="border-b border-border last:border-b-0">
      <!-- A title alone is centred in the 56 px row, or it would sit on an empty band above the next hairline. -->
      <label
        class="flex min-h-14 gap-3 py-3 {described ? 'items-start' : 'items-center'} {framed ? 'px-4' : ''} {o.disabled ? 'cursor-not-allowed' : 'cursor-pointer'}"
      >
        <input
          bind:this={inputs[i]}
          type="radio"
          {name}
          value={o.value}
          checked={selected}
          disabled={o.disabled}
          class="{described ? 'mt-0.5' : ''} size-4 shrink-0 cursor-[inherit] accent-accent"
          aria-labelledby="{uid}-{i}-title"
          aria-describedby={described ? `${uid}-${i}-desc` : undefined}
          onchange={() => choose(o.value)}
          onkeydown={(e) => key(e, i)}
        />
        <span class="min-w-0">
          <span id="{uid}-{i}-title" class="block text-body font-medium {o.disabled ? 'text-faint' : 'text-text'}">{o.title}</span>
          {#if described}
            <span id="{uid}-{i}-desc" class="block text-small text-muted">
              {o.description ?? ''}{#if o.description && o.disabled && o.reason}{' '}{/if}{#if o.disabled && o.reason}{o.reason}{/if}
            </span>
          {/if}
        </span>
      </label>
      {#if selected && expanded && o.expandable !== false}
        <!-- Indented to the title: 16 px row padding + 16 px radio + 12 px gap. -->
        <div class="border-t border-border pt-3 pb-4 {framed ? 'mr-4 ml-11 max-sm:ml-4' : 'ml-7'}">{@render expanded(o.value)}</div>
      {/if}
    </div>
  {/each}
</div>
