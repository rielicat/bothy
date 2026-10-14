<script lang="ts">
  import AdvancedField from '../../../components/AdvancedField.svelte';
  import { SettingsSection } from '../../../components/ui';
  import { config } from '../../../lib/config.svelte';

  // Refine (F3) arrives with agent.refine.*; until a server has it the rows stay hidden.
  const refine = $derived(!!(config.entry('agent.refine.drafts') || config.schema('agent.refine.drafts')));
</script>

<SettingsSection title="Limits">
  <h3 class="text-body font-medium text-text">Each task</h3>
  <AdvancedField path="budgets.run.max_steps" label="Steps" type="number" />
  <AdvancedField path="budgets.run.max_time" label="Time" type="duration" />
  <AdvancedField path="budgets.run.max_usd" label="Cost" type="money" help="0 means no limit for one task." />
  <AdvancedField path="budgets.run.max_tokens" label="Tokens" type="number" />

  <h3 class="mt-8 text-body font-medium text-text">Each scheduled task</h3>
  <AdvancedField path="budgets.scheduled.max_steps" label="Steps" type="number" />
  <AdvancedField path="budgets.scheduled.max_time" label="Time" type="duration" />
  <AdvancedField path="budgets.scheduled.max_usd" label="Cost" type="money" />
  <AdvancedField path="budgets.scheduled.max_tokens" label="Tokens" type="number" />

  <h3 class="mt-8 text-body font-medium text-text">Check-ins and messages</h3>
  <AdvancedField path="budgets.heartbeat_daily_usd" label="Check-in spending per day" type="money" />
  <AdvancedField path="budgets.sender_rate.per_minute" label="Messages per minute from one sender" type="number" />
  <AdvancedField path="budgets.sender_rate.burst" label="Messages in a burst" type="number" />
  <AdvancedField path="budgets.loop.warn_after" label="Warn after repeated steps" type="number" />
  <AdvancedField path="budgets.loop.abort_after" label="Stop after repeated steps" type="number" />

  {#if refine}
    <h3 class="mt-8 text-body font-medium text-text">Refine</h3>
    <AdvancedField path="agent.refine.drafts" label="Drafts" type="number" />
    <AdvancedField path="agent.refine.depth" label="Rounds" type="number" />
    <AdvancedField path="agent.refine.early_stop" label="Stop early when drafts agree" type="switch" />
  {/if}
</SettingsSection>
