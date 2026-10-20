<script lang="ts">
  import type { Session } from '../../lib/api/types.gen';
  import { relTime } from '../../lib/format';
  import { router } from '../../lib/router.svelte';
  import { clock } from '../../lib/stores/clock.svelte';
  import Dot from '../ui/Dot.svelte';
  import IconButton from '../ui/IconButton.svelte';
  import Menu from '../ui/Menu.svelte';
  import Spinner from '../ui/Spinner.svelte';
  import { chats, groupChats, type ChatRow } from './chats.svelte';

  const uid = $props.id();
  const current = $derived(router.current?.pattern === '/chat/:id' ? router.current.params.id : '');
  const view = $derived(groupChats(chats.rows, clock.now, chats.shown));
  // The family the open chat sits in opens, unless it was closed during this visit; it never closes itself.
  $effect(() => {
    const r = chats.rows.find((x) => x.kids.some((k) => k.id === current));
    if (r) chats.opened[r.key] ??= true;
  });

  let menuFor = $state('');
  // A closed family shows its members' strongest state: waiting for a decision beats working.
  const stateOf = (ss: Session[]) => (ss.some((s) => chats.waiting(s.id)) ? 'waiting' : ss.some((s) => chats.running[s.id]) ? 'running' : '');
</script>

<!-- `r` is set on the head of a family; `label` names a schedule's earlier result by its date. -->
{#snippet row(s: Session, r?: ChatRow, label?: string)}
  {@const title = chats.title(s)}
  {@const open = r && chats.isOpen(r.key)}
  {@const members = r && !open ? [s, ...r.kids] : [s]}
  {@const menuOpen = menuFor === s.id}
  {@const state = stateOf(members)}
  {@const related = stateOf([s]) === state ? '' : ' in a related chat'}
  {@const hold = r && !state}
  <div class="group flex items-center rounded-md {members.some((m) => m.id === current) ? 'bg-raised shadow-raised' : menuOpen ? 'bg-hover' : 'hover:bg-hover'}">
    <a
      href="/chat/{encodeURIComponent(s.id)}"
      onclick={router.link}
      aria-current={s.id === current ? 'page' : undefined}
      {title}
      class="flex h-8 min-w-0 flex-1 items-center rounded-md px-2.5 text-body text-text focus-visible:-outline-offset-2 pointer-coarse:h-11"
      ><span class="truncate">{label || title}</span></a
    >
    {#if r}
      <IconButton
        icon="chevron-right"
        size="sm"
        variant="quiet"
        label="Related chats for “{title}”"
        aria-expanded={open}
        aria-controls="{uid}-{r.key}"
        class="w-6 hover:bg-transparent active:bg-transparent pointer-coarse:w-11 {open ? 'rotate-90' : ''}"
        onclick={() => chats.toggle(r.key)}
      />
    {/if}
    <!-- The ⋯ takes the glyph's place on hover; touch has no hover, so there both show. On a head
         the ⋯ keeps its room while hidden and the glyph takes as much, so the toggle never moves under the pointer. -->
    {#if state}
      <span class="shrink-0 items-center justify-end pr-2 {r ? 'w-8' : ''} {menuOpen ? 'hidden' : 'flex group-focus-within:hidden group-hover:hidden'} pointer-coarse:flex!">
        {#if state === 'running'}
          <Spinner size={12} label="Bothy is working{related}" />
        {:else}
          <Dot label="Waiting for approval{related}" />
        {/if}
      </span>
    {/if}
    <Menu
      placement="bottom-end"
      bind:open={() => menuOpen, (v) => (menuFor = v ? s.id : menuFor === s.id ? '' : menuFor)}
      items={[
        { label: 'Rename', onselect: () => (chats.renaming = s) },
        { label: 'Delete chat…', danger: true, onselect: () => (chats.deleting = s) },
      ]}
    >
      {#snippet trigger(props)}
        <IconButton
          {...props}
          icon="more"
          size="sm"
          variant="quiet"
          label="Options for {title}"
          class="shrink-0 hover:bg-transparent active:bg-transparent {menuOpen ? '' : hold ? 'invisible group-focus-within:visible group-hover:visible pointer-coarse:visible' : 'hidden group-focus-within:inline-flex group-hover:inline-flex'} pointer-coarse:inline-flex"
        />
      {/snippet}
    </Menu>
  </div>
{/snippet}

{#each view.groups as g (g.key)}
  <p id="{uid}-{g.key}" class="mt-4 mb-1 px-2 text-caption text-muted first:mt-2">{g.label}</p>
  <ul aria-labelledby="{uid}-{g.key}">
    {#each g.items as r (r.key)}
      <li>
        {@render row(r.head, r.kids.length ? r : undefined)}
        {#if r.kids.length && chats.isOpen(r.key)}
          <ul id="{uid}-{r.key}" aria-label="Related to “{chats.title(r.head)}”" class="ml-3 border-l border-border-strong pl-2">
            {#each r.kids as k (k.id)}
              <li>{@render row(k, undefined, r.key === `s:${k.schedule_id}` ? relTime(k.created_at, { now: clock.now }) : '')}</li>
            {/each}
          </ul>
        {/if}
      </li>
    {/each}
  </ul>
{/each}

{#if view.more}
  <button
    type="button"
    class="mt-1 flex h-8 w-full cursor-pointer items-center rounded-md px-2 text-left text-body text-muted hover:bg-hover hover:text-text focus-visible:-outline-offset-2 pointer-coarse:h-11"
    onclick={() => chats.showMore()}>Show more</button
  >
{/if}
