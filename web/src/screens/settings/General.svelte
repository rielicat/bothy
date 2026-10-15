<script lang="ts">
  import { untrack } from 'svelte';
  import { Button, Combobox, Disclosure, SettingsRow, SettingsSection, StatusLine, TextArea } from '../../components/ui';
  import { zoneOptions } from '../../lib/schedules';
  import { session } from '../../lib/stores/session.svelte';
  import { moneyText, parseMoney } from './accountHelpers';
  import SelectSetting from './SelectSetting.svelte';
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
  const peerWords: Record<string, string> = { local: 'this computer', lan: 'your local network', public: 'the internet' };
  let connectionDetails = $state(false);

  // The server's rule for agent.name (config.ValidAgentName), said before the save is refused.
  function agentName(s: string): { value?: string; error?: string } {
    const t = s.trim();
    if (!t) return { error: 'Enter a name.' };
    if ([...t].length > 40) return { error: 'Use at most 40 characters.' };
    if (/[<>]/.test(t)) return { error: 'Leave out < and >.' };
    return { value: t };
  }

  const tones = [
    { value: 'warm', label: 'Warm and brief' },
    { value: 'direct', label: 'Straight to the point' },
    { value: 'formal', label: 'Calm and formal' },
  ];

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
        <!-- Open Details make the value several lines tall; the label stays on the first one. -->
        <div class={connectionDetails ? '[&>div]:sm:items-start' : ''}>
          <SettingsRow label="Connection">
            {#snippet children(c)}
              <output id={c.id} class="block text-body {connection.warn ? 'text-warning-text' : 'text-muted'}">{connection.text}</output>
              {#if connection.warn && access}
                <Disclosure class="mt-1" bind:open={connectionDetails}>
                  <dl class="grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 text-small text-muted">
                    <dt>Reached from</dt>
                    <dd class="text-text">{peerWords[access.peer] ?? access.peer} ({access.peer})</dd>
                    <dt>HTTPS</dt>
                    <dd class="text-text">No</dd>
                  </dl>
                  <p class="mt-2 text-small text-muted">Put Bothy behind HTTPS, for example with Tailscale or Caddy, before your team uses it from other computers.</p>
                </Disclosure>
              {/if}
            {/snippet}
          </SettingsRow>
        </div>
      {/if}
    </SettingsSection>

    <SettingsSection title="Spending">
      <TextSetting
        label="Daily limit"
        path="budgets.daily_usd"
        prefix="$"
        inputmode="decimal"
        help="Bothy stops starting new work when everyone's spending reaches this. It resets at midnight."
        format={moneyText}
        parse={dailyLimit}
      />
    </SettingsSection>

    <SettingsSection title="Bothy">
      <TextSetting label="Name" path="agent.name" parse={agentName} />
      <SelectSetting label="Tone" path="agent.tone" options={tones} fallback="warm" />
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
            <span class="text-muted" aria-live="polite">{rulesSaver.status === 'saving' ? 'Saving…' : rulesSaver.status === 'saved' ? 'Saved' : ''}</span>
          {/snippet}
        </TextArea>
      </div>
      <SettingsRow label="Personality">
        {#snippet children()}<Button href="/settings/general/personality">Edit personality</Button>{/snippet}
      </SettingsRow>
    </SettingsSection>
  {/if}
</SettingsFrame>
