<script lang="ts">
  import Icon from '../../components/Icon.svelte';
  import Verbatim from '../../components/Verbatim.svelte';
  import { CodeBlock, DiffView, Spinner } from '../../components/ui';
  import { outsideSource } from '../../lib/activity';
  import { duration } from '../../lib/format';
  import { router } from '../../lib/router.svelte';
  import type { StepLine } from './steps';
  import { clampLines } from './tasks';

  // `next`: the way forward from this step, when the viewer has one ("Review it").
  // `gutter`: some step in the list has a state glyph, so every row keeps its column and the words line up.
  let { line, how, next, gutter = true }: { line: StepLine; how: string; next?: { label: string; href: string }; gutter?: boolean } = $props();

  const uid = $props.id();
  let open = $state(false);
  const call = $derived(line.call);
  const tone = $derived(line.state === 'waiting' ? 'text-warning-text' : 'text-text');
</script>

<!-- A row of the task's group card (§10.2); the chevron says it opens. -->
<li class="border-b border-border">
  <div class="flex items-start gap-3">
  <button
    type="button"
    class="-mx-2 flex min-h-11 min-w-0 flex-1 cursor-pointer items-start gap-2 rounded-md px-2 py-2.5 text-left text-body {tone} hover:bg-subtle focus-visible:-outline-offset-2 pointer-coarse:min-h-12"
    aria-expanded={open}
    aria-controls="{uid}-d"
    onclick={() => (open = !open)}
  >
    {#if gutter}
      <span class="flex h-5 w-4 shrink-0 items-center justify-center text-muted">
        {#if line.state === 'running'}<Spinner size={12} />
        {:else if line.state === 'waiting'}<Icon name="clock" class="text-warning-text" />
        {:else if line.state === 'failed'}<Icon name="alert" class="text-danger-text" />
        {:else if line.state === 'denied'}<Icon name="close" />{/if}
      </span>
    {/if}
    <span class="min-w-0 flex-1 [overflow-wrap:anywhere]" data-copy-ignore
      >{#each line.segments as s, i (i)}{#if s.code}<code class="text-code"><Verbatim text={s.text} /></code>{:else}{s.text}{/if}{/each}</span
    >
    <span class="flex h-5 shrink-0 items-center text-muted"><Icon name="chevron-right" size={12} class="transition-transform duration-160 {open ? 'rotate-90' : ''}" /></span>
  </button>
  {#if next}
    <a class="link inline-flex min-h-11 shrink-0 items-center py-2.5 text-body whitespace-nowrap pointer-coarse:min-h-12" href={next.href} onclick={router.link}>{next.label}</a>
  {/if}
  </div>
  <div id="{uid}-d" data-details class="pb-3 text-small text-muted {gutter ? 'pl-6' : ''}" hidden={!open}>
    {#if open}
      {#if call}
        <!-- The chat's order (§7.4): result, how it was allowed, the exact request, then the technical line. -->
        {#if call.action?.diff}
          <p class="mt-1 mb-1">Result</p>
          <DiffView diff={call.action.diff} />
        {:else if call.result_preview}
          <CodeBlock class="mt-1" label="Result" code={call.result_preview} wrap maxLines={clampLines(call.result_preview, 20)} />
        {/if}
        {#if how}<p class="mt-3">How it was allowed: {how}</p>{/if}
        {#if call.action}
          {@const asked = JSON.stringify(call.action.diff ? { ...call.action, diff: '(shown above)' } : call.action, null, 2)}
          <CodeBlock class="mt-3" label="What Bothy asked for" code={asked} wrap maxLines={clampLines(asked, 12)} />
        {/if}
        <p class="mt-3">
          Technical: Tool {call.tool}{how ? '' : ` · Decision ${call.verdict}`}{call.rule ? ` · Rule ${call.rule}` : ''}{call.duration ? ` · Took ${duration(call.duration)}` : ''}{call.status === 'ok' &&
          call.action?.host
            ? ` · Contacted ${call.action.host}`
            : ''}
        </p>
        {#if call.untrusted}<p class="mt-1">This came from outside your team ({outsideSource(call.tool)}). Bothy treats it with caution.</p>{/if}
      {:else if line.approval}
        {#if line.approval.decision?.reason}<p class="mt-1">Why Bothy asked: {line.approval.decision.reason}</p>{/if}
        <p class="mt-1">Approval {line.approval.id}</p>
      {/if}
    {/if}
  </div>
</li>
