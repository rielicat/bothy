<script lang="ts">
  import { router } from '../../lib/router.svelte';
  import { clock } from '../../lib/stores/clock.svelte';
  import Dot from '../ui/Dot.svelte';
  import IconButton from '../ui/IconButton.svelte';
  import Menu from '../ui/Menu.svelte';
  import Spinner from '../ui/Spinner.svelte';
  import { chats, groupChats } from './chats.svelte';

  const uid = $props.id();
  const current = $derived(router.current?.pattern === '/chat/:id' ? router.current.params.id : '');
  const view = $derived(groupChats(chats.visible, new Set(chats.spaces.keys()), clock.now, chats.shown));

  let menuFor = $state('');
</script>

{#each view.groups as g (g.key)}
  <p id="{uid}-{g.key}" class="mt-4 mb-1 px-2 text-caption text-muted">{g.label}</p>
  <ul aria-labelledby="{uid}-{g.key}">
    {#each g.items as s (s.id)}
      {@const title = chats.title(s)}
      {@const here = s.id === current}
      {@const open = menuFor === s.id}
      <li class="group flex items-center rounded-md {here ? 'bg-selected' : 'hover:bg-hover'}">
        <a
          href="/chat/{encodeURIComponent(s.id)}"
          onclick={router.link}
          aria-current={here ? 'page' : undefined}
          {title}
          class="flex h-8 min-w-0 flex-1 items-center rounded-md pl-2 text-body text-text focus-visible:-outline-offset-2 pointer-coarse:h-11"
          ><span class="truncate">{title}</span></a
        >
        <!-- The state glyph and the ⋯ share one slot; on touch both show, since there is no hover. -->
        <span class="h-8 w-8 shrink-0 items-center justify-center pointer-coarse:h-11 {open ? 'hidden' : 'flex group-focus-within:hidden group-hover:hidden'} pointer-coarse:flex!">
          {#if chats.running[s.id]}
            <Spinner size={12} label="Bothy is working" />
          {:else if chats.waiting(s.id)}
            <Dot label="Waiting for approval" />
          {/if}
        </span>
        <Menu
          placement="bottom-end"
          bind:open={() => open, (v) => (menuFor = v ? s.id : menuFor === s.id ? '' : menuFor)}
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
              class="shrink-0 {open ? '' : 'hidden group-focus-within:inline-flex group-hover:inline-flex'} pointer-coarse:inline-flex"
            />
          {/snippet}
        </Menu>
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
