<script lang="ts" module>
  export interface ComboboxOption {
    value: string;
    label: string;
    description?: string;
  }

  /** Options whose label contains the query, ignoring case and accents. */
  export function filterOptions(options: ComboboxOption[], query: string): ComboboxOption[] {
    const fold = (s: string) => s.normalize('NFD').replace(/\p{M}/gu, '').toLowerCase();
    const q = fold(query.trim());
    return q ? options.filter((o) => fold(o.label).includes(q)) : options;
  }
</script>

<script lang="ts">
  import type { Snippet } from 'svelte';
  import Icon from '../Icon.svelte';
  import FieldMessage from './FieldMessage.svelte';
  import { floating, popoverAttr } from './layer';

  let {
    label,
    value = $bindable(),
    options,
    multiple = false,
    placeholder,
    help,
    error,
    id: idProp,
    describedBy: outer,
    onchange,
    class: cls = '',
  }: {
    label: string;
    /** One value, or the chosen values when `multiple`. */
    value?: string | string[];
    options: ComboboxOption[];
    multiple?: boolean;
    placeholder?: string;
    help?: string | Snippet;
    error?: string;
    id?: string;
    /** Help or an error shown outside the field, such as a settings row's. */
    describedBy?: string;
    onchange?: (value: string | string[]) => void;
    class?: string;
  } = $props();

  const uid = $props.id();
  const id = $derived(idProp ?? `${uid}-input`);
  const listId = `${uid}-list`;
  const optId = (i: number) => `${uid}-opt-${i}`;

  let input: HTMLInputElement | undefined = $state();
  let open = $state(false);
  let active = $state(-1);

  const chosen = $derived(multiple ? (Array.isArray(value) ? value : []) : typeof value === 'string' && value ? [value] : []);
  const labelOf = (v: string) => options.find((o) => o.value === v)?.label ?? v;
  const selectedLabel = $derived(!multiple && chosen[0] ? labelOf(chosen[0]) : '');

  let query = $state('');
  // A single choice shows its label in the field until the person types.
  $effect(() => {
    if (!multiple && !open) query = selectedLabel;
  });

  const filtered = $derived(!multiple && query === selectedLabel ? options : filterOptions(options, query));
  const describedBy = $derived(
    [outer ?? '', error ? `${uid}-error` : help ? `${uid}-help` : '', multiple && chosen.length ? `${uid}-chosen` : ''].filter(Boolean).join(' ') || undefined,
  );

  function show() {
    if (open) return;
    open = true;
    const i = filtered.findIndex((o) => chosen.includes(o.value));
    active = i >= 0 ? i : filtered.length ? 0 : -1;
  }

  function hide() {
    open = false;
    active = -1;
    if (multiple) query = '';
  }

  function choose(o: ComboboxOption) {
    if (multiple) {
      const next = chosen.includes(o.value) ? chosen.filter((v) => v !== o.value) : [...chosen, o.value];
      value = next;
      onchange?.(next);
      query = '';
      active = Math.max(0, options.indexOf(o));
    } else {
      if (o.value !== value) {
        value = o.value;
        onchange?.(o.value);
      }
      query = o.label;
      hide();
    }
  }

  function scrollActive() {
    document.getElementById(optId(active))?.scrollIntoView({ block: 'nearest' });
  }

  function key(e: KeyboardEvent) {
    switch (e.key) {
      case 'ArrowDown':
        e.preventDefault();
        if (!open) show();
        else active = Math.min(filtered.length - 1, active + 1);
        scrollActive();
        return;
      case 'ArrowUp':
        e.preventDefault();
        if (!open) show();
        else active = Math.max(0, active - 1);
        scrollActive();
        return;
      case 'Enter':
        if (open && active >= 0 && filtered[active]) {
          e.preventDefault();
          choose(filtered[active]);
        }
        return;
      case 'Escape':
        if (open) {
          e.preventDefault();
          e.stopPropagation();
          hide();
          if (!multiple) query = selectedLabel;
        }
        return;
      case 'Backspace':
        if (multiple && !query && chosen.length) {
          const next = chosen.slice(0, -1);
          value = next;
          onchange?.(next);
        }
        return;
      case 'Tab':
        hide();
        return;
    }
  }

  function typed() {
    show();
    active = filtered.length ? 0 : -1;
  }
</script>

<div class={cls}>
  <label for={id} class="field-label">{label}</label>
  <div class="relative mt-2">
    <input
      bind:this={input}
      bind:value={query}
      {id}
      type="text"
      role="combobox"
      class="input pr-8"
      autocomplete="off"
      spellcheck="false"
      aria-autocomplete="list"
      aria-expanded={open}
      aria-controls={open ? listId : undefined}
      aria-activedescendant={open && active >= 0 ? optId(active) : undefined}
      aria-invalid={error ? 'true' : undefined}
      aria-describedby={describedBy}
      {placeholder}
      oninput={typed}
      onclick={() => (open ? hide() : show())}
      onkeydown={key}
      onblur={() => {
        hide();
        if (!multiple) query = selectedLabel;
      }}
    />
    <Icon name="chevron-down" class="pointer-events-none absolute top-1/2 right-2.5 -translate-y-1/2 text-muted" />
  </div>
  {#if multiple && chosen.length}
    <p id="{uid}-chosen" class="mt-1 text-small text-text">{chosen.map(labelOf).join(', ')}</p>
  {/if}
  <FieldMessage id={uid} {help} {error} />
</div>

{#if open}
  <!-- Options keep focus in the field: mousedown is cancelled so the field never blurs mid-choice. -->
  <div
    popover={popoverAttr}
    class="fixed inset-auto z-50 m-0 max-h-80 max-w-[calc(100vw-1rem)] overflow-y-auto rounded-lg border border-border bg-surface p-1 text-text shadow-overlay"
    {@attach floating(() => input, 'bottom-start', { matchWidth: true })}
  >
    <div id={listId} role="listbox" aria-label={label} aria-multiselectable={multiple || undefined}>
      {#each filtered as o, i (o.value)}
        {@const on = chosen.includes(o.value)}
        <!-- svelte-ignore a11y_click_events_have_key_events -->
        <div
          id={optId(i)}
          role="option"
          tabindex="-1"
          aria-selected={on}
          class="flex min-h-8 cursor-pointer items-start gap-2 rounded-md px-2 py-1.5 text-body pointer-coarse:min-h-11 pointer-coarse:items-center {i === active
            ? 'bg-hover'
            : ''}"
          onmousedown={(e) => e.preventDefault()}
          onpointermove={() => (active = i)}
          onclick={() => choose(o)}
        >
          <span class="flex h-5 w-4 shrink-0 items-center">{#if on}<Icon name="check" />{/if}</span>
          <span class="min-w-0 flex-1">
            <span class="block">{o.label}</span>
            {#if o.description}<span class="block text-small text-muted">{o.description}</span>{/if}
          </span>
        </div>
      {/each}
    </div>
    {#if !filtered.length}<p class="px-2 py-1.5 text-body text-muted">No matches</p>{/if}
  </div>
{/if}
