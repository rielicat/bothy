<script lang="ts">
  import { untrack } from 'svelte';
  import { events } from '../../lib/sse.svelte';
  import { announce } from '../ui/live.svelte';
  import Spinner from '../ui/Spinner.svelte';

  // Live updates lost (docs/design/ui.md §13.2): a muted line, never a banner.
  // The first connection says nothing; only losing an open one does.
  type State = 'hidden' | 'reconnecting' | 'offline' | 'connected';
  const text: Record<State, string> = {
    hidden: '',
    reconnecting: 'Reconnecting…',
    offline: "You're offline. This page will update when you're back.",
    connected: 'Connected',
  };

  let shown: State = $state('hidden');
  let fading = $state(false);
  let wasOpen = false;
  let timers: ReturnType<typeof setTimeout>[] = [];

  function set(s: State) {
    timers.forEach(clearTimeout);
    timers = [];
    if (s !== shown && s !== 'hidden') announce(text[s]);
    shown = s;
    fading = false;
    if (s === 'reconnecting') timers.push(setTimeout(() => set('offline'), 30_000));
    if (s === 'connected') timers.push(setTimeout(() => (fading = true), 1800), setTimeout(() => set('hidden'), 2000));
  }

  $effect(() => {
    const st = events.status;
    untrack(() => {
      if (st === 'open') {
        if (shown === 'reconnecting' || shown === 'offline') set('connected');
        wasOpen = true;
      } else if (st === 'idle') {
        wasOpen = false;
        set('hidden');
      } else if (wasOpen && shown !== 'reconnecting' && shown !== 'offline') set('reconnecting');
    });
  });

  $effect(() => () => timers.forEach(clearTimeout));
</script>

{#if shown !== 'hidden'}
  <div class="bg-bg">
    <p class="mx-auto flex max-w-3xl items-center gap-2 px-6 py-2 text-small text-muted transition-opacity duration-200 max-sm:px-4 {fading ? 'opacity-0' : ''}">
      {#if shown === 'reconnecting'}<Spinner size={12} />{/if}{text[shown]}
    </p>
  </div>
{/if}
