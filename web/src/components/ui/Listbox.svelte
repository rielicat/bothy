<script lang="ts" module>
  import type { Attachment } from 'svelte/attachments';

  export interface ListboxOption {
    value: string;
    label: string;
    description?: string;
    group?: string;
  }

  export type ListboxTriggerProps = {
    id: string;
    'aria-haspopup': 'listbox';
    'aria-expanded': boolean;
    'aria-controls': string | undefined;
    onclick: (e: MouseEvent) => void;
    onkeydown: (e: KeyboardEvent) => void;
    [key: symbol]: Attachment<HTMLElement>;
  };
</script>

<script lang="ts">
  import { tick, type Snippet } from 'svelte';
  import { createAttachmentKey } from 'svelte/attachments';
  import Icon from '../Icon.svelte';
  import { findTypeahead, floating, outside, popoverAttr, typeahead, type Placement } from './layer';

  let {
    value = $bindable(),
    options,
    trigger,
    label,
    placement = 'bottom-start',
    onchange,
  }: {
    value?: string;
    options: ListboxOption[];
    /** Receives the props to spread on the trigger button and the selected option for its text. */
    trigger: Snippet<[ListboxTriggerProps, ListboxOption | undefined]>;
    /** Names the list; defaults to the trigger's text. */
    label?: string;
    placement?: Placement;
    onchange?: (value: string) => void;
  } = $props();

  const uid = $props.id();
  const attachKey = createAttachmentKey();
  let anchor: HTMLElement | null = null;
  let list: HTMLElement | undefined = $state();
  let open = $state(false);
  let active = $state(0);
  const search = typeahead();

  const selected = $derived(options.find((o) => o.value === value));
  const optId = (i: number) => `${uid}-opt-${i}`;

  type Group = { group?: string; items: { o: ListboxOption; i: number }[] };
  const groups = $derived.by(() => {
    const out: Group[] = [];
    options.forEach((o, i) => {
      const last = out[out.length - 1];
      if (last && last.group === o.group) last.items.push({ o, i });
      else out.push({ group: o.group, items: [{ o, i }] });
    });
    return out;
  });

  async function show() {
    active = Math.max(0, options.findIndex((o) => o.value === value));
    open = true;
    await tick();
    list?.focus();
    scrollActive();
  }

  function close(returnFocus: boolean) {
    open = false;
    if (returnFocus) anchor?.focus();
  }

  function choose(i: number) {
    const o = options[i];
    if (!o) return;
    if (o.value !== value) {
      value = o.value;
      onchange?.(o.value);
    }
    close(true);
  }

  function scrollActive() {
    document.getElementById(optId(active))?.scrollIntoView({ block: 'nearest' });
  }

  function move(i: number) {
    active = Math.max(0, Math.min(options.length - 1, i));
    scrollActive();
  }

  function listKey(e: KeyboardEvent) {
    switch (e.key) {
      case 'ArrowDown':
        e.preventDefault();
        move(active + 1);
        return;
      case 'ArrowUp':
        e.preventDefault();
        move(active - 1);
        return;
      case 'Home':
        e.preventDefault();
        move(0);
        return;
      case 'End':
        e.preventDefault();
        move(options.length - 1);
        return;
      case 'Enter':
      case ' ':
        e.preventDefault();
        choose(active);
        return;
      case 'Escape':
        e.preventDefault();
        e.stopPropagation();
        close(true);
        return;
      case 'Tab':
        close(false);
        return;
    }
    if (e.key.length === 1 && !e.ctrlKey && !e.metaKey && !e.altKey) {
      const j = findTypeahead(
        options.map((o) => o.label),
        search(e.key),
        active,
      );
      if (j >= 0) move(j);
    }
  }

  const setAnchor = (el: HTMLElement) => {
    anchor = el;
    return () => {
      if (anchor === el) anchor = null;
    };
  };

  const triggerProps: ListboxTriggerProps = $derived({
    id: `${uid}-trigger`,
    'aria-haspopup': 'listbox',
    'aria-expanded': open,
    'aria-controls': open ? `${uid}-list` : undefined,
    onclick: () => (open ? close(false) : show()),
    onkeydown: (e: KeyboardEvent) => {
      if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
        e.preventDefault();
        void show();
      }
    },
    [attachKey]: setAnchor,
  });
</script>

{@render trigger(triggerProps, selected)}

{#if open}
  <!-- Focus stays on the list; aria-activedescendant names the active option. -->
  <div
    bind:this={list}
    id="{uid}-list"
    role="listbox"
    tabindex="-1"
    aria-label={label}
    aria-labelledby={label ? undefined : `${uid}-trigger`}
    aria-activedescendant={optId(active)}
    popover={popoverAttr}
    class="group fixed inset-auto z-50 m-0 max-h-80 max-w-[calc(100vw-1rem)] min-w-48 overflow-y-auto rounded-lg border border-border bg-surface p-1 text-text shadow-overlay transition-[opacity,translate] duration-120 ease-out focus-visible:outline-none starting:open:-translate-y-1 starting:open:opacity-0"
    onkeydown={listKey}
    {@attach floating(() => anchor, placement)}
    {@attach outside(
      () => [list, anchor],
      () => close(false),
    )}
  >
    {#each groups as g, gi (gi)}
      <div role={g.group ? 'group' : 'none'} aria-labelledby={g.group ? `${uid}-g${gi}` : undefined}>
        {#if g.group}<div id="{uid}-g{gi}" class="px-2 pt-2 pb-1 text-caption text-muted">{g.group}</div>{/if}
        {#each g.items as { o, i } (o.value)}
          <!-- svelte-ignore a11y_click_events_have_key_events -->
          <div
            id={optId(i)}
            role="option"
            tabindex="-1"
            aria-selected={o.value === value}
            class="flex min-h-8 cursor-pointer items-start gap-2 rounded-md px-2 py-1.5 text-body pointer-coarse:min-h-11 pointer-coarse:items-center {i === active
              ? 'bg-hover group-focus-visible:outline-2 group-focus-visible:-outline-offset-2 group-focus-visible:outline-focus'
              : ''}"
            onclick={() => choose(i)}
            onpointermove={() => (active = i)}
          >
            <span class="flex h-5 w-4 shrink-0 items-center">{#if o.value === value}<Icon name="check" />{/if}</span>
            <span class="min-w-0 flex-1">
              <span class="block">{o.label}</span>
              {#if o.description}<span class="block text-small text-muted">{o.description}</span>{/if}
            </span>
          </div>
        {/each}
      </div>
    {/each}
  </div>
{/if}
