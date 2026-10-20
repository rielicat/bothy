<svelte:options runes />

<script lang="ts">
  import type { Attachment } from 'svelte/attachments';
  import { shell } from '../../lib/stores/ui.svelte';
  import Dialog from '../ui/Dialog.svelte';
  import Kbd from '../ui/Kbd.svelte';
  import { isMac } from './keys';

  // docs/design/ui.md §6.4, the only place keys are drawn.
  const mac = isMac();
  const sections: { title?: string; rows: { label: string; keys: string[][] }[] }[] = [
    {
      rows: [
        { label: 'Search', keys: [mac ? ['⌘', 'K'] : ['Ctrl', 'K'], ['/']] },
        { label: 'New chat', keys: [mac ? ['⌘', '⇧', 'O'] : ['Ctrl', 'Shift', 'O']] },
        { label: 'Stop Bothy (in a running chat) or close', keys: [['Esc']] },
      ],
    },
    {
      title: 'In Approvals',
      rows: [
        { label: 'Approve', keys: [['A']] },
        { label: 'Allow for this chat', keys: [['S']] },
        { label: 'Deny', keys: [['D']] },
        { label: 'Move', keys: [['J'], ['K']] },
        { label: 'Open', keys: [['Enter']] },
      ],
    },
  ];

  // Nothing here takes input, so focus starts on the list rather than on Close,
  // whose focus ring and tooltip would be the first thing seen. The timeout
  // runs after the dialog's own initial focus.
  const focusFirst: Attachment<HTMLElement> = (el) => {
    const t = setTimeout(() => el.focus({ preventScroll: true }));
    return () => clearTimeout(t);
  };
</script>

<Dialog bind:open={shell.shortcuts} title="Keyboard shortcuts">
  <div tabindex="-1" class="outline-none" {@attach focusFirst}>
    {#each sections as s, i (i)}
      {#if s.title}<h3 class="mt-5 mb-1 text-caption text-muted">{s.title}</h3>{/if}
      <dl>
        {#each s.rows as r (r.label)}
          <div class="flex min-h-10 items-center justify-between gap-4 border-b border-border py-2 last:border-b-0">
            <dt class="text-body text-text">{r.label}</dt>
            <dd class="flex shrink-0 items-center gap-2 text-small text-muted">
              {#each r.keys as k, j (j)}{#if j > 0}<span>or</span>{/if}<Kbd keys={k} />{/each}
            </dd>
          </div>
        {/each}
      </dl>
    {/each}
  </div>
</Dialog>
