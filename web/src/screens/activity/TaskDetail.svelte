<script lang="ts">
  import Markdown from '../../components/Markdown.svelte';
  import { Button, CodeBlock, Disclosure, EmptyState, LinkButton, Page } from '../../components/ui';
  import { api, ApiError } from '../../lib/api/client';
  import type { MessagePage, Schedule, ToolCallRecord } from '../../lib/api/types.gen';
  import { date, duration, fullTime, money, plural } from '../../lib/format';
  import { where } from '../../lib/taskStatus';
  import { people, personName, type TaskDetail } from './api';
  import Loading from './Loading.svelte';
  import StepItem from './StepItem.svelte';
  import { allowedHow, decisionLine, stepLine, type StepLine } from './steps';
  import { dayLabel } from './tasks';

  let { params }: { params: Record<string, string> } = $props();

  let d = $state<TaskDetail | null>(null);
  let title = $state('');
  let chatOrigin = $state<string | undefined>();
  let answer = $state('');
  let names = $state(new Map<string, string>());
  let missing = $state(false);
  let failed = $state(false);
  let all = $state(false);
  let clamp: HTMLElement | undefined = $state();
  let long = $state(false);

  async function load() {
    const id = params.id;
    failed = missing = false;
    try {
      const [detail, n] = await Promise.all([api.get<TaskDetail>(`/api/runs/${encodeURIComponent(id)}`), people()]);
      names = n;
      const run = detail.run;
      if (run) {
        const [chat, schedule, page] = await Promise.all([
          api.get<{ title: string; origin: string }>(`/api/sessions/${encodeURIComponent(run.session_id)}`).catch(() => null),
          run.schedule_id && !run.schedule_name ? api.get<Schedule>(`/api/schedules/${encodeURIComponent(run.schedule_id)}`).catch(() => null) : null,
          detail.answer ? null : api.get<MessagePage>(`/api/sessions/${encodeURIComponent(run.session_id)}/messages?limit=100`).catch(() => null),
        ]);
        const scheduled = run.trigger === 'schedule' || run.trigger === 'heartbeat';
        title = scheduled ? run.schedule_name || schedule?.name || 'A schedule' : run.chat_title || chat?.title || 'A chat';
        chatOrigin = run.origin ?? chat?.origin;
        const reply = page?.messages?.filter((m) => m?.run_id === run.id && m.role === 'assistant').pop();
        answer = detail.answer ?? reply?.parts?.map((p) => (p.t === 'text' ? (p.text ?? '') : '')).join('') ?? '';
      }
      d = detail;
    } catch (e) {
      if (e instanceof ApiError && e.status === 404) missing = true;
      else failed = true;
    }
  }

  $effect(() => {
    void params.id;
    d = null;
    void load();
  });

  const run = $derived(d?.run);
  const calls = $derived((d?.tool_calls ?? []).filter((t): t is ToolCallRecord => !!t && t.tool !== 'tool_search' && t.tool !== 'use_tool'));
  const approvalFor = (t: ToolCallRecord) => d?.approvals?.find((a) => a?.id === t.approval_id) ?? undefined;
  const lines = $derived.by(() => {
    const out: { line: StepLine; how: string }[] = [];
    for (const t of calls) {
      const a = approvalFor(t);
      const who = personName(a?.decided_by, names);
      const decided = a && a.status !== 'pending' && a.status !== 'parked' ? decisionLine(a, who) : null;
      if (decided) out.push({ line: decided, how: '' });
      out.push({ line: stepLine(t), how: allowedHow(t, a, who) });
    }
    return out;
  });

  const took = $derived.by(() => {
    const s = date(run?.started_at)?.getTime();
    const f = date(run?.finished_at)?.getTime();
    return s && f && f >= s ? `Took ${duration((f - s) * 1e6)}` : '';
  });
  const refine = $derived(d?.refine);
  const agreed = $derived(refine ? `${refine.votes[0]?.count ?? 1} of ${refine.drafts} agreed` : '');
  const meta = $derived.by(() => {
    if (!run) return '';
    const scheduled = run.trigger === 'schedule' || run.trigger === 'heartbeat';
    const t = new Date(run.created_at);
    return [
      scheduled ? 'Ran on a schedule' : `${personName(run.user_id, names, run.requested_by_name)} asked in ${where(chatOrigin, run.trigger)}`,
      `${dayLabel(run.created_at)} at ${t.toLocaleTimeString(undefined, { hour: 'numeric', minute: '2-digit' })}`,
      took,
      run.cost_micros ? `Cost ${money(run.cost_micros)}` : '',
      refine ? `Refined from ${plural(refine.drafts, 'draft')} · ${agreed}` : '',
    ]
      .filter(Boolean)
      .join(' · ');
  });

  $effect(() => {
    void answer;
    if (clamp) long = clamp.scrollHeight > clamp.clientHeight + 1;
  });

  const tokens = (u: { input: number; cache_read: number; cache_write: number; output: number }) => `${(u.input + u.cache_write).toLocaleString()} / ${u.cache_read.toLocaleString()} / ${u.output.toLocaleString()}`;
</script>

{#snippet openChat()}
  {#if run}<Button href="/chat/{encodeURIComponent(run.session_id)}">Open chat</Button>{/if}
{/snippet}

{#snippet facts(rows: [string, string][])}
  <table class="mt-2 w-full text-small">
    <tbody>
      {#each rows.filter((r) => r[1]) as [k, v] (k)}
        <tr class="border-b border-border last:border-b-0">
          <th scope="row" class="w-44 py-1.5 pr-4 text-left align-top font-normal text-muted max-sm:w-32">{k}</th>
          <td class="py-1.5 align-top text-text [overflow-wrap:anywhere]">{v}</td>
        </tr>
      {/each}
    </tbody>
  </table>
{/snippet}

{#if missing}
  <Page title="Task not found" back={{ href: '/activity', label: 'Activity' }}>
    <EmptyState text="This task doesn't exist or was deleted." action={{ label: 'Go to Activity', href: '/activity' }} />
  </Page>
{:else if !d || !run}
  <Page title="Task" back={{ href: '/activity', label: 'Activity' }}><Loading {failed} retry={load} /></Page>
{:else}
  <Page {title} back={{ href: '/activity', label: 'Activity' }} actions={openChat}>
    <p class="-mt-4 text-small text-muted"><time datetime={run.created_at} title={fullTime(run.created_at)}>{meta}</time></p>

    {#if lines.length}
      <section class="mt-10">
        <h2 class="section-title">What Bothy did</h2>
        <ul class="mt-2">
          {#each lines as l (l.line.key)}<StepItem line={l.line} how={l.how} />{/each}
        </ul>
      </section>
    {/if}

    {#if answer}
      <section class="mt-10">
        <h2 class="section-title">Answer</h2>
        <div bind:this={clamp} class="mt-2 text-reading {all ? '' : 'line-clamp-3'}"><Markdown text={answer} /></div>
        {#if long || all}<LinkButton class="mt-1 text-small" onclick={() => (all = !all)}>{all ? 'Show less' : 'Show all'}</LinkButton>{/if}
      </section>
    {/if}

    <Disclosure label="Technical details" class="mt-10">
      {#each d.llm_calls ?? [] as c, i (c.id)}
        <h3 class="mt-4 text-small font-medium text-text">Model call {i + 1}</h3>
        {@render facts([
          ['Model', `${c.ref.provider}/${c.ref.model}`],
          ['Purpose', c.purpose],
          ['Time to first word', c.ttft ? duration(c.ttft) : ''],
          ['Duration', duration(c.latency)],
          ['Tokens in / cached / out', tokens(c.usage)],
          ['Cost', c.price_known ? money(c.cost_micros) : 'Price not set'],
          ['Fallback attempts', c.attempts?.length > 1 ? c.attempts.map((a) => `${a.ref.provider}/${a.ref.model}${a.err ? `: ${a.err}` : ''}`).join('; ') : ''],
        ])}
      {/each}
      {#each calls as t, i (t.id)}
        <h3 class="mt-4 text-small font-medium text-text">Tool call {i + 1}</h3>
        {@render facts([
          ['Tool', t.tool],
          ['Decision', t.verdict],
          ['Rule', t.rule],
          ['Approval id', t.approval_id ?? ''],
          ['Sites contacted', t.status === 'ok' ? (t.action?.host ?? '') : ''],
          ['Duration', t.duration ? duration(t.duration) : ''],
        ])}
        {#if t.action}<CodeBlock class="mt-2" label="Exact action" code={JSON.stringify(t.action, null, 2)} wrap maxLines={8} />{/if}
      {/each}
      {#if refine}
        <h3 class="mt-4 text-small font-medium text-text">Refine</h3>
        <p class="mt-1 text-small text-text">{plural(refine.drafts, 'draft')}, {plural(refine.rounds, 'round')}, {agreed}</p>
        {#each refine.entries ?? [] as e, i (i)}
          <Disclosure class="mt-1" label="{e.role === 'draft' ? 'Draft' : e.role === 'critique' ? 'Critique' : e.role === 'correct' ? 'Correction' : 'Continuation'} {e.draft + 1}{e.round ? `, round ${e.round}` : ''}">
            <CodeBlock code={e.text} wrap maxLines={12} />
          </Disclosure>
        {/each}
      {/if}
      <CodeBlock class="mt-4" label="Task id" code={run.id} />
    </Disclosure>
  </Page>
{/if}
