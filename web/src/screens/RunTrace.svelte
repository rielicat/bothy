<script lang="ts">
  import Icon from '../components/Icon.svelte';
  import StackedBar from '../components/StackedBar.svelte';
  import Verbatim from '../components/Verbatim.svelte';
  import { api, errorText, qs } from '../lib/api/client';
  import type { CallInfo, EgressRecord, List, RunDetail, ToolCallRecord } from '../lib/api/types.gen';
  import { bytes, date, duration, tokens, usd, when } from '../lib/format';
  import { choiceDone, statusOf } from '../lib/labels';
  import { router } from '../lib/router.svelte';
  import { describe } from '../lib/transcript';

  let { params }: { params: Record<string, string> } = $props();

  let d = $state<RunDetail | null>(null);
  let egress = $state<EgressRecord[]>([]);
  let error = $state('');

  type Step = { at: number; llm?: CallInfo; tool?: ToolCallRecord };

  const steps = $derived.by(() => {
    if (!d) return [] as Step[];
    const out: Step[] = [
      ...(d.llm_calls ?? []).map((c) => ({ at: c.step * 2, llm: c })),
      ...(d.tool_calls ?? []).filter((t): t is ToolCallRecord => !!t).map((t) => ({ at: t.step * 2 + 1, tool: t })),
    ];
    return out.sort((a, b) => a.at - b.at);
  });

  const run = $derived(d?.run);
  const span = $derived.by(() => {
    const s = date(run?.started_at)?.getTime();
    const f = date(run?.finished_at)?.getTime();
    return s && f ? duration((f - s) * 1e6) : '';
  });

  $effect(() => {
    const id = params.id;
    d = null;
    error = '';
    api
      .get<RunDetail>(`/api/runs/${encodeURIComponent(id)}`)
      .then(async (r) => {
        d = r;
        if (r.run) {
          const e = await api.get<List<EgressRecord>>(`/api/egress${qs({ session: r.run.session_id, limit: 200 })}`).catch(() => null);
          egress = (e?.items ?? []).filter((x) => x.run_id === id);
        }
      })
      .catch((e) => (error = errorText(e)));
  });

  const approvalFor = (t: ToolCallRecord) => d?.approvals?.find((a) => a?.id === t.approval_id);
</script>

<svelte:head><title>Run trace · Bothy</title></svelte:head>

<div class="page max-w-4xl">
  <a href={run ? `/chat/${encodeURIComponent(run.session_id)}` : '/chat'} onclick={router.link} class="mb-4 inline-flex items-center gap-1 text-sm text-muted hover:text-fg">
    <Icon name="left" size={15} />Back to the chat
  </a>
  {#if error}
    <p class="alert-bad" role="alert">{error}</p>
  {:else if !d || !run}
    <div class="space-y-3" aria-busy="true"><div class="skeleton h-8 w-1/3"></div><div class="skeleton h-28"></div></div>
  {:else}
    <div class="mb-6 flex flex-wrap items-center gap-3">
      <h1 class="page-title">Run trace</h1>
      <span class={statusOf(run.status).cls}>{statusOf(run.status).label}</span>
      <span class="chip">{run.trigger}</span>
      <code class="text-xs text-faint">{run.id}</code>
    </div>
    {#if run.error}<p class="alert-bad mb-4">{run.error}</p>{/if}
    <dl class="mb-8 grid grid-cols-2 gap-3 sm:grid-cols-4">
      <div class="stat"><dt>Cost</dt><dd>{usd(run.cost_micros)}</dd></div>
      <div class="stat"><dt>Tokens</dt><dd>{tokens(run.usage.input + run.usage.cache_read + run.usage.cache_write + run.usage.output)}</dd></div>
      <div class="stat"><dt>Steps · tools</dt><dd>{run.steps} · {run.tool_calls}</dd></div>
      <div class="stat"><dt>Duration</dt><dd>{span || '—'}</dd></div>
    </dl>
    <p class="mb-6 text-sm text-muted">
      Started {when(run.started_at)}{run.models?.length ? ` · ${run.models.join(', ')}` : ''}
      {#if run.schedule_id}· <a class="link" href="/schedules/{encodeURIComponent(run.schedule_id)}" onclick={router.link}>schedule</a>{/if}
    </p>

    <ol class="relative space-y-3 border-l border-line pl-6">
      {#each steps as s, i (i)}
        <li class="relative">
          <span class="absolute top-4 -left-[31px] grid size-4 place-items-center rounded-full border-2 border-bg {s.llm ? 'bg-accent' : 'bg-info'}"></span>
          {#if s.llm}
            {@const c = s.llm}
            <div class="card p-4">
              <div class="flex flex-wrap items-center gap-2 text-sm">
                <Icon name="spark" size={15} class="text-accent" />
                <span class="font-medium">{c.ref.provider}/{c.ref.model}</span>
                <span class="chip">step {c.step} · {c.purpose}</span>
                {#if c.error}<span class="chip chip-bad">error</span>{/if}
                <span class="ml-auto tabular-nums">{c.price_known ? usd(c.cost_micros) : 'price unknown'}</span>
              </div>
              <p class="mt-1 text-xs text-muted">
                {tokens(c.usage.input)} in · {tokens(c.usage.cache_read)} cached · {tokens(c.usage.output)} out · TTFT {duration(c.ttft)} · total {duration(c.latency)}
                {#if c.usage_estimated}· estimated{/if}
              </p>
              {#if c.breakdown?.window}<div class="mt-3"><StackedBar b={c.breakdown} /></div>{/if}
              {#if (c.attempts?.length ?? 0) > 1}
                <ul class="mt-3 space-y-1 text-xs">
                  {#each c.attempts as a, j (j)}
                    <li class="flex gap-2 {a.err ? 'text-bad' : 'text-ok'}">
                      <Icon name={a.err ? 'x' : 'check'} size={13} />{a.ref.provider}/{a.ref.model}
                      <span class="text-muted">{a.err ? `${a.kind ?? 'error'}: ${a.err}` : 'answered'} · {duration(a.latency)}</span>
                    </li>
                  {/each}
                </ul>
              {/if}
              {#if c.error}<p class="mt-2 text-xs text-bad">{c.error}</p>{/if}
            </div>
          {:else if s.tool}
            {@const t = s.tool}
            {@const ap = approvalFor(t)}
            <details class="card p-4">
              <summary class="flex cursor-pointer flex-wrap items-center gap-2 text-sm">
                <Icon name="terminal" size={15} class="text-info" />
                <span class="min-w-0 flex-1 font-medium"><Verbatim text={describe(t.tool, t.action)} /></span>
                <span class={statusOf(t.status === 'ok' ? 'done' : t.status === 'error' ? 'failed' : t.status).cls}>{t.status}</span>
                <span class="text-xs text-muted tabular-nums">{duration(t.duration)}</span>
              </summary>
              <div class="mt-3 space-y-3 text-sm">
                <p class="flex flex-wrap gap-2 text-xs">
                  <span class="chip">{t.side_effect}</span>
                  <span class="chip">{t.verdict}{t.rule ? ` · ${t.rule}` : ''}</span>
                  {#if ap}<span class="chip {ap.status === 'allowed' ? 'chip-ok' : 'chip-bad'}">{ap.choice ? choiceDone[ap.choice] : ap.status}{ap.decided_via ? ` via ${ap.decided_via}` : ''}</span>{/if}
                  {#if t.untrusted}<span class="chip chip-warn">untrusted output</span>{/if}
                </p>
                {#if t.action}
                  <pre class="mono max-h-64 overflow-auto rounded-lg bg-sunken p-3 whitespace-pre-wrap break-all"><Verbatim text={JSON.stringify(t.action, null, 2)} /></pre>
                {/if}
                {#if t.result_preview}
                  <div>
                    <p class="mb-1 text-xs text-muted">Result · {bytes(t.result_bytes)}{t.full_ref ? ` · full output in ${t.full_ref}` : ''}</p>
                    <pre class="mono max-h-64 overflow-auto rounded-lg bg-sunken p-3 whitespace-pre-wrap break-all"><Verbatim text={t.result_preview} /></pre>
                  </div>
                {/if}
              </div>
            </details>
          {/if}
        </li>
      {/each}
    </ol>

    {#if egress.length}
      <h2 class="mt-10 mb-3 section-title">Network destinations</h2>
      <div class="card overflow-x-auto">
        <table class="table">
          <thead><tr><th>Host</th><th>Decision</th><th>Up / down</th></tr></thead>
          <tbody>
            {#each egress as e (e.id)}
              <tr>
                <td class="mono">{e.host}:{e.port}</td>
                <td><span class="chip {e.decision.includes('allow') ? 'chip-ok' : 'chip-bad'}">{e.decision}</span>{#if e.reason}<span class="ml-2 text-xs text-muted">{e.reason}</span>{/if}</td>
                <td class="tabular-nums">{bytes(e.bytes_up)} / {bytes(e.bytes_down)}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  {/if}
</div>
