<script lang="ts">
  import { tick } from 'svelte';
  import { router } from '../../lib/router.svelte';
  import { session } from '../../lib/stores/session.svelte';
  import { shell, theme, type ThemePref } from '../../lib/stores/ui.svelte';
  import Icon from '../Icon.svelte';
  import Avatar from '../ui/Avatar.svelte';
  import { findTypeahead, floating, outside, popoverAttr, typeahead } from '../ui/layer';

  // The footer account row and its menu (docs/design/ui.md §4.2), with Theme
  // as a submenu. The F2 Menu has no submenus, so this one is its own.
  const uid = $props.id();
  const name = $derived(session.me?.user.display_name || session.me?.user.username || '');

  let open = $state(false);
  let sub = $state(false);
  let trigger: HTMLButtonElement | undefined = $state();
  let menu: HTMLElement | undefined = $state();
  let subMenu: HTMLElement | undefined = $state();
  let themeItem: HTMLElement | undefined = $state();
  const search = typeahead();

  type Item = { label: string; run?: () => void; theme?: true; danger?: boolean; disabled?: boolean; reason?: string } | 'separator';

  // Shortcut help is for keyboards: touch screens never show it (§2, rule 7).
  const keyboard = !window.matchMedia || window.matchMedia('(hover: hover) and (pointer: fine)').matches;

  const items: Item[] = $derived([
    { label: 'Your account', run: () => router.navigate('/settings/account') },
    { label: 'Theme', theme: true },
    ...(keyboard ? [{ label: 'Keyboard shortcuts', run: () => (shell.shortcuts = true) }] : []),
    'separator',
    ...(session.isAdmin ? [{ label: 'Pause Bothy…', danger: true, disabled: session.paused, reason: 'Bothy is already paused.', run: () => (shell.pause = true) }] : []),
    { label: 'Sign out', run: () => void session.logout() },
  ]);
  const themes: { v: ThemePref; label: string }[] = [
    { v: 'system', label: 'System' },
    { v: 'light', label: 'Light' },
    { v: 'dark', label: 'Dark' },
  ];

  const els = (root: HTMLElement | undefined) => [...(root?.querySelectorAll<HTMLElement>('[data-item]') ?? [])];
  const focusAt = (root: HTMLElement | undefined, i: number) => {
    const list = els(root);
    if (list.length) list[((i % list.length) + list.length) % list.length].focus();
  };

  async function show(at: number) {
    open = true;
    await tick();
    focusAt(menu, at);
  }

  function close(focusTrigger: boolean) {
    open = false;
    sub = false;
    if (focusTrigger) trigger?.focus();
  }

  async function openSub(focus: boolean) {
    sub = true;
    if (!focus) return;
    await tick();
    focusAt(
      subMenu,
      themes.findIndex((t) => t.v === theme.pref),
    );
  }

  function closeSub() {
    sub = false;
    themeItem?.focus();
  }

  function choose(it: Exclude<Item, 'separator'>) {
    if (it.disabled) return;
    if (it.theme) return void openSub(true);
    close(true);
    it.run?.();
  }

  function keys(e: KeyboardEvent, root: HTMLElement | undefined, labels: string[], inSub: boolean) {
    const list = els(root);
    const i = list.indexOf(document.activeElement as HTMLElement);
    const moves: Record<string, number> = { ArrowDown: i + 1, ArrowUp: i < 0 ? -1 : i - 1, Home: 0, End: -1 };
    if (e.key in moves) {
      e.preventDefault();
      focusAt(root, moves[e.key]);
    } else if (e.key === 'Escape') {
      e.preventDefault();
      e.stopPropagation();
      if (inSub) closeSub();
      else close(true);
    } else if (e.key === 'ArrowLeft' && inSub) {
      e.preventDefault();
      closeSub();
    } else if (e.key === 'ArrowRight' && !inSub && document.activeElement === themeItem) {
      e.preventDefault();
      void openSub(true);
    } else if (e.key === 'Tab') {
      close(false);
    } else if (e.key.length === 1 && e.key !== ' ' && !e.ctrlKey && !e.metaKey && !e.altKey) {
      const j = findTypeahead(labels, search(e.key), i);
      if (j >= 0) focusAt(root, j);
    }
  }

  // Beside the menu when there is room, otherwise over it: phones have no room to the side.
  function beside(item: () => HTMLElement | undefined) {
    return (el: HTMLElement) => {
      if (typeof el.showPopover === 'function' && el.hasAttribute('popover')) el.showPopover();
      const a = item()?.getBoundingClientRect();
      const m = menu?.getBoundingClientRect();
      if (!a || !m) return;
      const r = el.getBoundingClientRect();
      const vw = document.documentElement.clientWidth || window.innerWidth;
      let left = m.right + 4;
      if (left + r.width > vw - 8) left = m.left - r.width - 4;
      if (left < 8) left = Math.max(8, vw - r.width - 8);
      const top = Math.max(8, Math.min(a.top - 4, window.innerHeight - r.height - 8));
      el.style.left = `${Math.round(left)}px`;
      el.style.top = `${Math.round(top)}px`;
    };
  }

  const itemClass =
    'flex min-h-8 w-full cursor-pointer items-center gap-2 rounded-md px-2 py-1.5 text-left text-body hover:bg-hover focus:bg-hover focus:outline-none focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-focus aria-disabled:cursor-not-allowed pointer-coarse:min-h-11';
  const panel =
    'fixed inset-auto z-50 m-0 min-w-48 rounded-lg border border-border bg-surface p-1 text-text shadow-[var(--shadow-overlay)] transition-[opacity,translate] duration-120 ease-out starting:open:-translate-y-1 starting:open:opacity-0';
</script>

<button
  bind:this={trigger}
  type="button"
  id="{uid}-trigger"
  aria-haspopup="menu"
  aria-expanded={open}
  aria-controls={open ? `${uid}-menu` : undefined}
  aria-label="Account: {name}"
  class="flex min-h-11 w-full cursor-pointer items-center gap-2 rounded-md px-2 text-left hover:bg-hover focus-visible:-outline-offset-2"
  onclick={() => (open ? close(false) : show(0))}
  onkeydown={(e) => {
    if (e.key === 'ArrowUp' || e.key === 'ArrowDown') {
      e.preventDefault();
      void show(e.key === 'ArrowDown' ? 0 : -1);
    }
  }}
>
  <!-- bg-subtle, the avatar's own fill, is almost the sidebar's colour. -->
  <Avatar {name} class="bg-selected!" />
  <span class="min-w-0 flex-1 truncate text-body font-medium text-text">{name}</span>
  <Icon name="more" class="text-muted" />
</button>

{#if open}
  <div
    bind:this={menu}
    id="{uid}-menu"
    role="menu"
    tabindex="-1"
    aria-labelledby="{uid}-trigger"
    popover={popoverAttr}
    class={panel}
    onkeydown={(e) =>
      keys(
        e,
        menu,
        items.filter((x) => x !== 'separator').map((x) => x.label),
        false,
      )}
    {@attach floating(() => trigger, 'top-start')}
    {@attach outside(
      () => [menu, subMenu, trigger],
      () => close(false),
    )}
  >
    {#each items as it, i (i)}
      {#if it === 'separator'}
        <div role="separator" class="mx-1 my-1 h-px bg-border"></div>
      {:else if it.theme}
        <button
          bind:this={themeItem}
          type="button"
          role="menuitem"
          tabindex="-1"
          data-item
          aria-haspopup="menu"
          aria-expanded={sub}
          class="{itemClass} text-text {sub ? 'bg-hover' : ''}"
          onclick={() => void openSub(true)}
          onpointermove={(e) => {
            e.currentTarget.focus();
            if (!sub) void openSub(false);
          }}
          ><span class="flex-1">Theme</span><Icon name="chevron-right" class="text-muted" /></button
        >
      {:else}
        <button
          type="button"
          role="menuitem"
          tabindex="-1"
          data-item
          aria-disabled={it.disabled || undefined}
          class="{itemClass} {it.disabled ? 'text-faint' : it.danger ? 'text-danger-text' : 'text-text'}"
          onclick={() => choose(it)}
          onpointermove={(e) => {
            e.currentTarget.focus();
            sub = false;
          }}
        >
          <span class="min-w-0 flex-1">
            <span class="block">{it.label}</span>
            {#if it.disabled && it.reason}<span class="block text-small text-muted">{it.reason}</span>{/if}
          </span>
        </button>
      {/if}
    {/each}
  </div>
  {#if sub}
    <div
      bind:this={subMenu}
      role="menu"
      tabindex="-1"
      aria-label="Theme"
      popover={popoverAttr}
      class={panel}
      onkeydown={(e) =>
        keys(
          e,
          subMenu,
          themes.map((t) => t.label),
          true,
        )}
      {@attach beside(() => themeItem)}
    >
      {#each themes as t (t.v)}
        <button
          type="button"
          role="menuitemradio"
          aria-checked={theme.pref === t.v}
          tabindex="-1"
          data-item
          class="{itemClass} text-text"
          onclick={() => {
            theme.set(t.v);
            close(true);
          }}
          onpointermove={(e) => e.currentTarget.focus()}
          ><span class="flex h-5 w-4 shrink-0 items-center">{#if theme.pref === t.v}<Icon name="check" />{/if}</span>{t.label}</button
        >
      {/each}
    </div>
  {/if}
{/if}
