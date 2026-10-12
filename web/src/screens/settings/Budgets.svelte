<script lang="ts">
  import ConfigField from '../../components/ConfigField.svelte';
  import Section from '../../components/Section.svelte';
  import { usd } from '../../lib/format';
  import { session } from '../../lib/stores/session.svelte';

  const bars = $derived(
    [
      { label: 'You today', spent: session.today?.spent_micros ?? 0, limit: session.today?.limit_micros ?? 0 },
      { label: 'Everyone today', spent: session.today?.global_spent_micros ?? 0, limit: session.today?.global_limit_micros ?? 0 },
    ].filter((b) => b.limit > 0),
  );

  $effect(() => {
    void session.refreshToday();
  });
</script>

{#if bars.length}
  <Section title="Today">
    {#each bars as b (b.label)}
      {@const r = Math.min(1, b.spent / b.limit)}
      <div class="py-3.5">
        <div class="mb-1.5 flex justify-between text-sm"><span>{b.label}</span><span class="tabular-nums">{usd(b.spent)} of {usd(b.limit)}</span></div>
        <div class="h-2 overflow-hidden rounded-full bg-sunken"><div class="h-full rounded-full {r >= 0.8 ? 'bg-warn' : 'bg-accent'}" style:width="{r * 100}%"></div></div>
      </div>
    {/each}
  </Section>
{/if}

<Section title="Daily caps" desc="Runs stop when a cap is reached; the cap resets at midnight in the instance timezone.">
  <ConfigField path="budgets.daily_usd" label="Everyone, per day" type="usd" help="0 means unlimited, which the security audit flags." />
  <ConfigField path="budgets.member_daily_usd" label="Each member, per day" type="usd" />
  <ConfigField path="budgets.heartbeat_daily_usd" label="Heartbeat, per day" type="usd" />
  <ConfigField path="budgets.warn_ratio" label="Warn at" help="Fraction of a cap, e.g. 0.8 for 80%." step={0.05} />
</Section>

<Section title="Per run: chats" desc="Limits for one reply, including all its tool steps.">
  <ConfigField path="budgets.run.max_steps" label="Steps" />
  <ConfigField path="budgets.run.max_tokens" label="Tokens" />
  <ConfigField path="budgets.run.max_time" label="Time" />
  <ConfigField path="budgets.run.max_usd" label="Cost" type="usd" help="0 means no per-run cap." />
</Section>

<Section title="Per run: schedules">
  <ConfigField path="budgets.scheduled.max_steps" label="Steps" />
  <ConfigField path="budgets.scheduled.max_tokens" label="Tokens" />
  <ConfigField path="budgets.scheduled.max_time" label="Time" />
  <ConfigField path="budgets.scheduled.max_usd" label="Cost" type="usd" />
</Section>

<Section title="Abuse protection">
  <ConfigField path="budgets.sender_rate.per_minute" label="Chat messages per minute per sender" />
  <ConfigField path="budgets.sender_rate.burst" label="Burst" />
  <ConfigField path="budgets.loop.warn_after" label="Warn after identical tool calls" />
  <ConfigField path="budgets.loop.abort_after" label="Stop after identical tool calls" />
</Section>
