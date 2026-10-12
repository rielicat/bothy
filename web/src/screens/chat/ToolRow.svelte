<script module lang="ts">
  import type { RunDetail } from '../../lib/api/types.gen';

  // Rows of one run share a single /api/runs fetch.
  const runs = new Map<string, Promise<RunDetail>>();
</script>

<script lang="ts">
  import Icon from '../../components/Icon.svelte';
  import Verbatim from '../../components/Verbatim.svelte';
  import { api } from '../../lib/api/client';
  import type { ToolCall, ToolCallRecord, ToolResult } from '../../lib/api/types.gen';
  import { bytes, duration, ms } from '../../lib/format';
  import { router } from '../../lib/router.svelte';
  import { describe, type LiveTool } from '../../lib/transcript';

  let { call, result, live, runId }: { call?: ToolCall; result?: ToolResult; live?: LiveTool; runId?: string } = $props();

  let open = $state(false);
  let record = $state<ToolCallRecord | null>(null);
  let loaded = false;

  const tool = $derived(live?.tool ?? call?.name ?? 'tool');
  const action = $derived(live?.action ?? record?.action);
  const label = $derived(describe(tool, action, call?.args, live?.title));
  const status = $derived(live?.status ?? record?.status ?? (result ? (result.is_error ? 'error' : 'ok') : 'running'));
  const running = $derived(status === 'running' || status === 'planned' || status === 'waiting' || status === 'approved');
  const took = $derived(live?.ms != null ? ms(live.ms) : record?.duration ? duration(record.duration) : '');
  const verdict = $derived.by(() => {
    const v = live?.verdict ?? record?.verdict;
    const rule = live?.rule ?? record?.rule ?? '';
    if (status === 'denied' || v === 'deny') return { text: 'denied', cls: 'chip chip-bad' };
    if (v === 'ask') return status === 'waiting' || status === 'planned' || (live && !live.status) ? { text: 'needs approval', cls: 'chip chip-warn' } : { text: 'approved', cls: 'chip chip-ok' };
    if (rule.startsWith('grant:')) return { text: 'granted', cls: 'chip chip-info' };
    return v ? { text: 'auto', cls: 'chip' } : null;
  });
  const untrusted = $derived(live?.untrusted ?? record?.untrusted ?? result?.untrusted ?? false);
  const preview = $derived(live?.preview ?? record?.result_preview ?? result?.content ?? '');
  const statusTone = $derived(
    status === 'ok' ? 'text-ok' : status === 'error' || status === 'timeout' || status === 'invalid' ? 'text-bad' : status === 'denied' || status === 'stopped' ? 'text-muted' : 'text-info',
  );

  async function toggle() {
    open = !open;
    if (!open || loaded || live || !runId || !call) return;
    loaded = true;
    try {
      let p = runs.get(runId);
      if (!p) runs.set(runId, (p = api.get<RunDetail>(`/api/runs/${encodeURIComponent(runId)}`)));
      const d = await p;
      record = (d.tool_calls ?? []).find((t) => t?.call_id === call.id) ?? null;
    } catch {
      // details are optional; the row still shows the arguments
    }
  }
</script>

<div class="group rounded-xl border border-line bg-surface/60 text-sm">
  <button type="button" class="flex w-full items-center gap-2 px-3 py-2 text-left" aria-expanded={open} onclick={toggle}>
    <span class="grid size-6 shrink-0 place-items-center rounded-md bg-sunken {statusTone}">
      {#if running}<span class="size-2 animate-pulse rounded-full bg-current"></span>{:else}<Icon
          name={status === 'ok' ? 'check' : status === 'denied' || status === 'stopped' ? 'x' : status === 'error' ? 'alert' : 'terminal'}
          size={14}
        />{/if}
    </span>
    <span class="min-w-0 flex-1 truncate"><Verbatim text={label} /></span>
    {#if untrusted}<span class="chip chip-warn max-sm:hidden" title="This output came from outside and is treated as untrusted">untrusted output</span>{/if}
    {#if verdict}<span class="{verdict.cls} max-sm:hidden">{verdict.text}</span>{/if}
    {#if took}<span class="text-xs text-faint tabular-nums">{took}</span>{/if}
    <Icon name="chevron" size={15} class="text-faint transition-transform {open ? 'rotate-180' : ''}" />
  </button>
  {#if open}
    <div class="space-y-3 border-t border-line px-3 py-3">
      <div class="flex flex-wrap gap-1.5 sm:hidden">
        {#if verdict}<span class={verdict.cls}>{verdict.text}</span>{/if}
        {#if untrusted}<span class="chip chip-warn">untrusted output</span>{/if}
      </div>
      <div>
        <p class="mb-1 text-xs font-medium text-muted">{action ? 'Exact action' : 'Arguments'}{action?.side_effect ? ` · ${action.side_effect}` : ''}</p>
        <pre class="mono max-h-56 overflow-auto rounded-lg bg-sunken p-2 whitespace-pre-wrap break-all"><Verbatim
            text={JSON.stringify(action ? { ...action, fingerprint: undefined } : (call?.args ?? {}), null, 2)}
          /></pre>
      </div>
      {#if live?.out.length}
        <div>
          <p class="mb-1 text-xs font-medium text-muted">Output</p>
          <pre class="mono max-h-64 overflow-auto rounded-lg bg-sunken p-2 whitespace-pre-wrap break-all">{#each live.out as o, i (i)}<span
                class={o.stream === 2 ? 'text-bad' : ''}><Verbatim text={o.chunk} /></span>{/each}</pre>
        </div>
      {/if}
      {#if preview}
        <div>
          <p class="mb-1 text-xs font-medium text-muted">
            Result{live?.bytes ? ` · ${bytes(live.bytes)}` : ''}{live?.truncated || result?.truncated ? ' · truncated' : ''}
          </p>
          <pre class="mono max-h-64 overflow-auto rounded-lg bg-sunken p-2 whitespace-pre-wrap break-all"><Verbatim text={preview} /></pre>
        </div>
      {/if}
      <p class="flex flex-wrap items-center gap-2 text-xs text-muted">
        <span>Status: <span class={statusTone}>{status}</span></span>
        {#if live?.rule || record?.rule}<span>· Rule: <code>{live?.rule || record?.rule}</code></span>{/if}
        {#if runId}<a href="/runs/{encodeURIComponent(runId)}" onclick={router.link} class="link ml-auto">Run trace</a>{/if}
      </p>
    </div>
  {/if}
</div>
