<script lang="ts">
  import type { Session } from '../../lib/api/types.gen';
  import { clock, money } from '../../lib/format';
  import { dayLabel } from '../../lib/transcript';
  import type { Conversation } from '../../screens/chat/conversation.svelte';
  import Disclosure from '../ui/Disclosure.svelte';
  import Drawer from '../ui/Drawer.svelte';
  import { appName } from './people.svelte';
  import TechnicalDetails from './TechnicalDetails.svelte';

  let {
    open = $bindable(false),
    conv,
    session,
    starter,
    model,
    modelId = '',
    refine,
    runId = '',
  }: { open?: boolean; conv: Conversation; session: Session; starter: string; model: string; modelId?: string; refine: boolean; runId?: string } = $props();

  let technical = $state(false);

  // Cost so far adds up every run of the chat, so their records are fetched when the drawer opens.
  $effect(() => {
    if (!open) return;
    const ids = [...new Set(conv.messages.map((m) => m.run_id).filter((x): x is string => !!x))].slice(-30);
    for (const id of ids) void conv.loadRun(id);
  });

  const cost = $derived(Object.values(conv.runs).reduce((s, d) => s + (d.run?.session_id === session.id ? d.run.cost_micros : 0), 0) + (conv.running ? (conv.usage?.run_cost_micros ?? 0) : 0));
  const breakdown = $derived(conv.usage?.call.breakdown ?? conv.preview);
  const used = $derived(breakdown ? breakdown.total / Math.max(1, breakdown.window) : 0);
  const calls = $derived(conv.runs[runId || conv.lastRunId]?.llm_calls ?? []);
</script>

<Drawer bind:open title="Chat details" onclose={() => (technical = false)}>
  <p class="text-body text-muted">
    Started by {starter} · {dayLabel(session.created_at)} at {clock(session.created_at)} · {appName[session.origin] ?? session.origin}
  </p>
  <dl class="mt-4 grid grid-cols-[7rem_minmax(0,1fr)] gap-x-4 gap-y-2 text-body">
    <dt class="text-muted">Model</dt>
    <dd>{model}</dd>
    <dt class="text-muted">Refine</dt>
    <dd>{refine ? 'On' : 'Off'}</dd>
    <dt class="text-muted">Cost so far</dt>
    <dd class="tabular-nums">{money(cost)}</dd>
    {#if used > 0.7}
      <dt class="text-muted">Length</dt>
      <dd>Getting long. Bothy will summarise older messages soon.</dd>
    {/if}
  </dl>
  <Disclosure label="Technical details" class="mt-6" bind:open={technical}>
    <TechnicalDetails {breakdown} {calls} chatId={session.id} {modelId} />
  </Disclosure>
</Drawer>
