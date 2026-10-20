<script lang="ts">
  import type { Session } from '../../lib/api/types.gen';
  import { clock, money } from '../../lib/format';
  import { channelName } from '../../lib/labels';
  import { dayLabel } from '../../lib/transcript';
  import type { Conversation } from '../../screens/chat/conversation.svelte';
  import Disclosure from '../ui/Disclosure.svelte';
  import Drawer from '../ui/Drawer.svelte';
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
  <p class="text-small text-muted">
    Started by {starter} · {dayLabel(session.created_at)} at {clock(session.created_at)} · {channelName[session.origin] ?? session.origin}
  </p>
  <!-- One group card of plain facts, as Settings shows them (§11.1). -->
  <dl class="list-card mt-3 text-body">
    {#snippet fact(label: string, value: string, cls = '')}
      <div class="row items-start justify-between gap-4 py-2.5">
        <dt class="shrink-0 text-muted">{label}</dt>
        <dd class="min-w-0 text-right {cls}">{value}</dd>
      </div>
    {/snippet}
    {@render fact('Model', model)}
    {@render fact('Refine', refine ? 'On' : 'Off')}
    {@render fact('Cost so far', money(cost), 'tabular-nums')}
    {#if used > 0.7}{@render fact('Length', 'Getting long. Bothy will summarise older messages soon.', 'text-pretty')}{/if}
  </dl>
  <Disclosure label="Technical details" class="mt-6" bind:open={technical}>
    <TechnicalDetails {breakdown} {calls} chatId={session.id} {modelId} />
  </Disclosure>
</Drawer>
