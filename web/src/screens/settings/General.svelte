<script lang="ts">
  import { untrack } from 'svelte';
  import Icon from '../../components/Icon.svelte';
  import { Button, Combobox, FieldError, ProgressBar, SettingsRow, SettingsSection, StatusLine, TextArea } from '../../components/ui';
  import { rowStatus } from '../../components/ui/saved.svelte';
  import { money } from '../../lib/format';
  import { zoneOptions } from '../../lib/schedules';
  import { session } from '../../lib/stores/session.svelte';
  import { moneyText, parseMoney } from './accountHelpers';
  import PictureSetting from './PictureSetting.svelte';
  import SettingsFrame from './SettingsFrame.svelte';
  import { helpFor, Saver, settingsConfig } from './store.svelte';
  import TextSetting from './TextSetting.svelte';

  const zone = $derived(settingsConfig.value<string>('timezone', 'UTC'));
  const zones = $derived(zoneOptions(zone));
  const zoneSaver = new Saver();
  let zoneValue = $state('');
  $effect.pre(() => {
    zoneValue = zone;
  });

  async function saveZone(v: string | string[]) {
    if (typeof v !== 'string' || v === zone) return;
    if (!(await zoneSaver.run(() => settingsConfig.save({ timezone: v })))) zoneValue = zone;
  }

  function webAddress(s: string): { value?: string; error?: string } {
    const t = s.trim().replace(/\/+$/, '');
    if (!t) return { value: '' };
    try {
      const u = new URL(t);
      if ((u.protocol === 'https:' || u.protocol === 'http:') && u.hostname) return { value: t };
    } catch {
      // falls through to the error
    }
    return { error: 'Use a full address, like https://bothy.acme.com.' };
  }

  function dailyLimit(s: string): { value?: number; error?: string } {
    const v = parseMoney(s);
    return v === null ? { error: 'Enter an amount in dollars, like 5.00.' } : { value: v };
  }

  // How this browser reached Bothy (dto.Access), for the Connection row.
  const access = $derived(session.me?.access);
  const connection = $derived.by(() => {
    if (!access) return null;
    if (access.tls) return { text: 'Reached securely over HTTPS', warn: false };
    if (access.peer === 'local') return { text: 'Only reachable from this computer', warn: false };
    return { text: 'Reached over plain HTTP from another computer', warn: true };
  });

  // The server's rule for agent.name (config.ValidAgentName), said before the save is refused.
  function agentName(s: string): { value?: string; error?: string } {
    const t = s.trim();
    if (!t) return { error: 'Enter a name.' };
    if ([...t].length > 40) return { error: 'Use at most 40 characters.' };
    if (/[<>]/.test(t)) return { error: 'Leave out < and >.' };
    return { value: t };
  }

  const tones = [
    { value: 'warm', label: 'Warm and brief', help: 'Friendly, short, first names.', sample: 'Good news: the release notes are ready for a look.' },
    { value: 'direct', label: 'Straight to the point', help: 'Just the answer.', sample: 'Release notes are ready to review.' },
    { value: 'formal', label: 'Calm and formal', help: 'Polished, for client work.', sample: 'Good morning. The release notes are ready for your review.' },
  ];
  const toneSaver = new Saver();
  const toneSaved = $derived(String(settingsConfig.value('agent.tone', 'warm')));
  let tone = $state('');
  $effect.pre(() => {
    tone = toneSaved;
  });
  async function saveTone() {
    if (!(await toneSaver.run(() => settingsConfig.save({ 'agent.tone': tone })))) tone = toneSaved;
  }

  // Today's spending against the limit. The limit is the field below, which is what the server
  // measures against, so the summary follows an edit at once rather than after a refresh.
  const spent = $derived(session.today?.global_spent_micros ?? 0);
  const limit = $derived(Math.round(Number(settingsConfig.value('budgets.daily_usd', 0)) * 1e6));
  // Fresh spending on open, and again when the limit changes (the spend-limit banner reads it too).
  $effect(() => {
    void limit;
    untrack(() => void session.refreshToday());
  });

  // Standing instructions are a list in the config, one instruction per line here.
  const rulesSaver = new Saver();
  const rulesText = $derived((settingsConfig.value<string[]>('agent.standing_rules', []) ?? []).join('\n'));
  let rules = $state('');
  let rulesBase = '';
  $effect.pre(() => {
    const t = rulesText;
    untrack(() => {
      if (rules === rulesBase) rules = t;
      rulesBase = t;
    });
  });

  async function saveRules() {
    if (rules === rulesBase) return;
    const list = rules
      .split('\n')
      .map((l) => l.trim())
      .filter(Boolean);
    const was = rulesBase;
    rulesBase = rules;
    if (!(await rulesSaver.run(() => settingsConfig.save({ 'agent.standing_rules': list })))) rulesBase = was;
  }
</script>

<SettingsFrame title="General" loading={!settingsConfig.view && !settingsConfig.failed}>
  {#if settingsConfig.failed}
    <StatusLine tone="danger" text="This page didn't load. Check your connection and try again." action={{ label: 'Try again', onclick: () => settingsConfig.load(true) }} />
  {:else if settingsConfig.view}
    <SettingsSection title="Workspace">
      <SettingsRow label="Time zone" help={helpFor('timezone', 'New people start with this time zone. The daily spending limit resets at midnight here.')} status={zoneSaver.status} error={zoneSaver.error}>
        {#snippet children()}
          <!-- The row's label is the visible one; the combobox keeps its own for screen readers. -->
          <Combobox label="Time zone" class="[&>div]:mt-0 [&>label]:sr-only" bind:value={zoneValue} options={zones} onchange={saveZone} />
        {/snippet}
      </SettingsRow>
      <TextSetting
        label="Web address"
        path="server.public_url"
        placeholder="https://bothy.acme.com"
        inputmode="url"
        autocomplete="url"
        help="The address your team uses. Links in chat apps point here."
        parse={webAddress}
      />
      {#if connection}
        <!-- The advice is the one useful part, so it shows under the warning instead of behind Details; the label stays on the first line. -->
        <div class={connection.warn ? '[&>div]:sm:items-start' : ''}>
          <SettingsRow label="Connection">
            {#snippet children(c)}
              <output id={c.id} class="block text-body {connection.warn ? 'text-warning-text' : 'text-muted'}">{connection.text}</output>
              {#if connection.warn}
                <p class="mt-1 text-small text-pretty text-muted">Put Bothy behind HTTPS, for example with Tailscale or Caddy, before your team uses it from other computers.</p>
              {/if}
            {/snippet}
          </SettingsRow>
        </div>
      {/if}
    </SettingsSection>

    <SettingsSection title="Spending">
      {#if limit}
        <div class="border-b border-border py-4">
          <p class="flex flex-wrap items-baseline justify-between gap-x-3">
            <span><span class="text-figure text-text tabular-nums">{money(spent)}</span> <span class="text-body text-muted">of {money(limit)} today</span></span><span class="text-small text-muted">Resets at midnight</span>
          </p>
          <ProgressBar class="mt-3" label="Today's spending" value={spent} max={limit} tone={spent >= limit ? 'danger' : spent >= 0.8 * limit ? 'warning' : 'default'} valueText="{money(spent)} of {money(limit)}" />
        </div>
      {/if}
      <TextSetting
        label="Daily limit"
        path="budgets.daily_usd"
        prefix="$"
        inputmode="decimal"
        help="Bothy stops starting new work when everyone's spending reaches this."
        format={moneyText}
        parse={dailyLimit}
      />
    </SettingsSection>

    <SettingsSection title="Bothy">
      <TextSetting label="Name" path="agent.name" parse={agentName} />
      <PictureSetting />
      <div class="border-b border-border py-3">
        <!-- "Saved" goes right after the label, as on the other rows (§11.1). -->
        <TextArea
          class="[&>div:first-child]:justify-start [&>div:first-child]:gap-x-2"
          label="Standing instructions"
          rows={4}
          bind:value={rules}
          placeholder="Always reply in British English. Our main product is the Acme web app."
          help={rulesSaver.error ? undefined : helpFor('agent.standing_rules', 'Bothy follows these in every chat.')}
          error={rulesSaver.error || undefined}
          onblur={saveRules}
        >
          {#snippet labelAction()}
            <span class="text-muted" aria-live="polite">{rowStatus(rulesSaver.status)}</span>
          {/snippet}
        </TextArea>
      </div>
      <SettingsRow label="Personality">
        {#snippet children()}<Button href="/settings/general/personality">Edit personality</Button>{/snippet}
      </SettingsRow>
    </SettingsSection>

    {#if settingsConfig.has('agent.tone')}
      <SettingsSection title="Tone" bare>
        <p class="-mt-1 mb-3 flex justify-between gap-3 text-small text-muted"><span id="tone-help">How Bothy sounds when it replies.</span><span aria-live="polite">{rowStatus(toneSaver.status)}</span></p>
        <div role="radiogroup" aria-describedby="tone-help" aria-label="Tone" class="grid gap-3 sm:grid-cols-3">
          {#each tones as t (t.value)}
            <label class="flex cursor-pointer flex-col rounded-lg border border-border-strong bg-surface p-4 hover:bg-subtle has-checked:border-accent has-checked:bg-accent-subtle has-focus-visible:outline-2 has-focus-visible:outline-offset-2 has-focus-visible:outline-focus">
              <input type="radio" name="tone" value={t.value} bind:group={tone} disabled={settingsConfig.locked('agent.tone')} class="sr-only" onchange={saveTone} />
              <span class="flex items-center justify-between gap-2 text-body font-medium text-text">{t.label}{#if tone === t.value}<Icon name="check" class="text-accent-text" />{/if}</span>
              <span class="text-small text-muted">{t.help}</span>
              <span class="mt-3 border-t border-border pt-3 text-small text-text">“{t.sample}”</span>
            </label>
          {/each}
        </div>
        <FieldError text={toneSaver.error} />
      </SettingsSection>
    {/if}
  {/if}
</SettingsFrame>
