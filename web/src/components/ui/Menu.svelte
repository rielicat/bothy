<script lang="ts" module>
  import type { Attachment } from 'svelte/attachments';

  export type MenuEntry = {
    label: string;
    onselect: () => void;
    /** Destructive: red text. */
    danger?: boolean;
    disabled?: boolean;
    /** Why it's disabled, shown under the label. */
    reason?: string;
    /** Set (true or false) for a choice among options; true draws the check. */
    checked?: boolean;
    /** A caption above consecutive items with the same group. */
    group?: string;
  };
  export type MenuItem = MenuEntry | 'separator';

  /** Spread onto the trigger button: `{#snippet trigger(props)}<IconButton {...props} … />{/snippet}`. */
  export type MenuTriggerProps = {
    id: string;
    'aria-haspopup': 'menu';
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
    trigger,
    items,
    placement = 'bottom-start',
    open = $bindable(false),
  }: {
    trigger: Snippet<[MenuTriggerProps]>;
    items: MenuItem[];
    placement?: Placement;
    open?: boolean;
  } = $props();

  const uid = $props.id();
  const attachKey = createAttachmentKey();
  let anchor: HTMLElement | null = null;
  let menu: HTMLElement | undefined = $state();
  const search = typeahead();

  const entries = $derived(items.filter((i): i is MenuEntry => i !== 'separator'));
  const hasChecks = $derived(entries.some((i) => i.checked !== undefined));

  // Consecutive items sharing a group are wrapped together under one caption.
  type Block = { group?: string; items: { item: MenuEntry; index: number }[] } | 'separator';
  const blocks = $derived.by(() => {
    const out: Block[] = [];
    let index = 0;
    for (const it of items) {
      if (it === 'separator') {
        out.push('separator');
        continue;
      }
      const last = out[out.length - 1];
      if (last && last !== 'separator' && last.group === it.group) last.items.push({ item: it, index: index++ });
      else out.push({ group: it.group, items: [{ item: it, index: index++ }] });
    }
    return out;
  });

  const itemEls = () => [...(menu?.querySelectorAll<HTMLElement>('[data-menu-item]') ?? [])];

  function focusItem(i: number) {
    const els = itemEls();
    if (!els.length) return;
    els[((i % els.length) + els.length) % els.length].focus();
  }

  async function show(at: 'first' | 'last') {
    open = true;
    await tick();
    focusItem(at === 'first' ? 0 : -1);
  }

  function close(returnFocus: boolean) {
    open = false;
    if (returnFocus) anchor?.focus();
  }

  function activate(item: MenuEntry) {
    if (item.disabled) return;
    // Focus goes back to the trigger first, so a dialog opened by the item returns focus there when it closes.
    close(true);
    item.onselect();
  }

  function menuKey(e: KeyboardEvent) {
    const els = itemEls();
    const i = els.indexOf(document.activeElement as HTMLElement);
    switch (e.key) {
      case 'ArrowDown':
        e.preventDefault();
        focusItem(i + 1);
        return;
      case 'ArrowUp':
        e.preventDefault();
        focusItem(i < 0 ? -1 : i - 1);
        return;
      case 'Home':
        e.preventDefault();
        focusItem(0);
        return;
      case 'End':
        e.preventDefault();
        focusItem(-1);
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
    if (e.key.length === 1 && !e.ctrlKey && !e.metaKey && !e.altKey && e.key !== ' ') {
      const j = findTypeahead(
        entries.map((x) => x.label),
        search(e.key),
        i,
      );
      if (j >= 0) focusItem(j);
    }
  }

  const setAnchor = (el: HTMLElement) => {
    anchor = el;
    return () => {
      if (anchor === el) anchor = null;
    };
  };

  const triggerProps: MenuTriggerProps = $derived({
    id: `${uid}-trigger`,
    'aria-haspopup': 'menu',
    'aria-expanded': open,
    'aria-controls': open ? `${uid}-menu` : undefined,
    onclick: () => (open ? close(false) : show('first')),
    onkeydown: (e: KeyboardEvent) => {
      if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
        e.preventDefault();
        void show(e.key === 'ArrowDown' ? 'first' : 'last');
      }
    },
    [attachKey]: setAnchor,
  });
</script>

{@render trigger(triggerProps)}

{#if open}
  <div
    bind:this={menu}
    id="{uid}-menu"
    role="menu"
    aria-labelledby="{uid}-trigger"
    tabindex="-1"
    popover={popoverAttr}
    class="fixed inset-auto z-50 m-0 max-h-[min(24rem,calc(100dvh-1rem))] max-w-[calc(100vw-1rem)] min-w-48 overflow-y-auto rounded-lg border border-border bg-surface p-1 text-text shadow-[var(--shadow-overlay)] transition-[opacity,translate] duration-120 ease-out starting:open:-translate-y-1 starting:open:opacity-0"
    onkeydown={menuKey}
    {@attach floating(() => anchor, placement)}
    {@attach outside(
      () => [menu, anchor],
      () => close(false),
    )}
  >
    {#each blocks as b, bi (bi)}
      {#if b === 'separator'}
        <div role="separator" class="mx-1 my-1 h-px bg-border"></div>
      {:else}
        <div role={b.group ? 'group' : 'none'} aria-labelledby={b.group ? `${uid}-g${bi}` : undefined}>
          {#if b.group}<div id="{uid}-g{bi}" class="px-2 pt-2 pb-1 text-caption text-muted">{b.group}</div>{/if}
          {#each b.items as { item, index } (index)}
            <button
              type="button"
              role={item.checked !== undefined ? 'menuitemradio' : 'menuitem'}
              aria-checked={item.checked}
              aria-disabled={item.disabled || undefined}
              tabindex="-1"
              data-menu-item
              class="flex min-h-8 w-full cursor-pointer items-start gap-2 rounded-md px-2 py-1.5 text-left text-body hover:bg-hover focus:bg-hover focus:outline-none focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-focus aria-disabled:cursor-not-allowed pointer-coarse:min-h-11 pointer-coarse:items-center {item.disabled
                ? 'text-faint'
                : item.danger
                  ? 'text-danger-text'
                  : 'text-text'}"
              onclick={() => activate(item)}
              onpointermove={(e) => e.currentTarget.focus()}
            >
              {#if hasChecks}<span class="flex h-5 w-4 shrink-0 items-center">{#if item.checked}<Icon name="check" />{/if}</span>{/if}
              <span class="min-w-0 flex-1">
                <span class="block">{item.label}</span>
                {#if item.disabled && item.reason}<span class="block text-small text-muted">{item.reason}</span>{/if}
              </span>
            </button>
          {/each}
        </div>
      {/if}
    {/each}
  </div>
{/if}
