<script lang="ts">
  import Icon from '../../components/Icon.svelte';
  import Verbatim from '../../components/Verbatim.svelte';
  import { CodeBlock, DiffView, Spinner } from '../../components/ui';
  import { duration } from '../../lib/format';
  import type { StepLine } from './steps';
  import { clampLines } from './tasks';

  let { line, how }: { line: StepLine; how: string } = $props();

  const uid = $props.id();
  let open = $state(false);
  const call = $derived(line.call);
  const tone = $derived(line.state === 'waiting' ? 'text-warning-text' : 'text-muted');
</script>

<li>
  <button type="button" class="flex min-h-8 w-full cursor-pointer items-start gap-2 py-1.5 text-left text-body {tone} hover:text-text pointer-coarse:min-h-11" aria-expanded={open} aria-controls="{uid}-d" onclick={() => (open = !open)}>
    <span class="flex h-5 w-4 shrink-0 items-center justify-center">
      {#if line.state === 'running'}<Spinner size={12} />
      {:else if line.state === 'waiting'}<Icon name="clock" />
      {:else if line.state === 'failed'}<Icon name="alert" class="text-danger-text" />
      {:else if line.state === 'denied'}<Icon name="close" />{/if}
    </span>
    <span class="min-w-0 [overflow-wrap:anywhere]" data-copy-ignore
      >{#each line.segments as s, i (i)}{#if s.code}<code class="text-code"><Verbatim text={s.text} /></code>{:else}{s.text}{/if}{/each}</span
    >
  </button>
  <div id="{uid}-d" data-details class="pb-2 pl-6 text-small text-muted" hidden={!open}>
    {#if open}
      {#if call}
        {#if call.action}{@const asked = JSON.stringify(call.action, null, 2)}<CodeBlock class="mt-1" label="What Bothy asked for" code={asked} wrap maxLines={clampLines(asked, 12)} />{/if}
        {#if call.action?.diff}
          <p class="mt-3 mb-1">Result</p>
          <DiffView diff={call.action.diff} />
        {:else if call.result_preview}
          <CodeBlock class="mt-3" label="Result" code={call.result_preview} wrap maxLines={clampLines(call.result_preview, 20)} />
        {/if}
        {#if how}<p class="mt-3">How it was allowed: {how}</p>{/if}
        <p class="mt-1">
          Technical: {call.tool} · {call.verdict}{call.rule ? ` · ${call.rule}` : ''}{call.duration ? ` · Took ${duration(call.duration)}` : ''}{call.status === 'ok' && call.action?.host ? ` · Contacted ${call.action.host}` : ''}
        </p>
        {#if call.untrusted}<p class="mt-1">This came from outside your team (a web page). Bothy treats it with caution.</p>{/if}
      {:else if line.approval}
        {#if line.approval.decision?.reason}<p class="mt-1">Why Bothy asked: {line.approval.decision.reason}</p>{/if}
        <p class="mt-1">Approval {line.approval.id}</p>
      {/if}
    {/if}
  </div>
</li>
