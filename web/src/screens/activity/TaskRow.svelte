<script lang="ts">
  import { router } from '../../lib/router.svelte';
  import { toneClass } from '../../lib/taskStatus';
  import type { TaskView } from './tasks';

  // Line 1: title, cost and time; line 2: who · where · outcome. On phones the
  // time joins line 2 and the cost stays on line 1 (§10.1).
  let { href, view, cost, time, fullTime }: { href: string; view: TaskView; cost: string; time: string; fullTime: string } = $props();
</script>

<li class="border-b border-border last:border-b-0">
  <a {href} onclick={router.link} class="row-link flex min-h-14 flex-col justify-center py-2 pointer-coarse:min-h-14">
    <span class="flex items-baseline gap-4">
      <span class="min-w-0 flex-1 truncate text-body font-medium text-text" title={view.title}>{view.title}</span>
      <span class="meta">{cost}</span>
      <time class="meta min-w-16 text-right whitespace-nowrap max-sm:hidden" title={fullTime}>{time}</time>
    </span>
    <span class="text-small text-muted sm:truncate">
      {view.lead.join(' · ')}{' · '}<span class={toneClass[view.outcome.tone]}>{view.outcome.text}</span><span class="sm:hidden">{' · '}{time}</span>
    </span>
  </a>
</li>
