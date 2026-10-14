<script lang="ts">
  import { api, errorText } from '../lib/api/client';
  import type { PairingCode, PairingLinkedData } from '../lib/api/types.gen';
  import { expiresIn } from '../lib/format';
  import { events } from '../lib/sse.svelte';
  import { clock } from '../lib/stores/clock.svelte';
  import Button from './ui/Button.svelte';
  import CommandBlock from './ui/CommandBlock.svelte';
  import LinkButton from './ui/LinkButton.svelte';
  import StatusLine from './ui/StatusLine.svelte';
  import QRCode from './QRCode.svelte';

  // A one-time code that links a chat app account to the signed-in person.
  // Used by Connections (step "Link your …") and Account → Linked chat apps.
  let {
    channel = '',
    bot = 'the bot',
    auto = false,
  }: {
    /** slack, telegram or discord; empty offers every app's way to link. */
    channel?: string;
    /** How the person finds Bothy in the app: "@bothy". */
    bot?: string;
    /** Creates the code on mount instead of waiting for "Link your account". */
    auto?: boolean;
  } = $props();

  let code = $state<PairingCode | null>(null);
  let linked = $state<PairingLinkedData | null>(null);
  let busy = $state(false);
  let error = $state('');

  const pretty = $derived(code ? `${code.code.slice(0, 4)}-${code.code.slice(4)}` : '');
  const link = $derived(Object.entries(code?.deep_links ?? {}).find(([c, u]) => (!channel || c === channel) && u.startsWith('https://')));
  const left = $derived(code ? expiresIn(code.expires_at, { now: clock.now }) : '');
  const command = $derived(channel === 'slack' ? `/bothy pair ${pretty}` : `/pair ${pretty}`);
  const apps: Record<string, string> = { telegram: 'Telegram', slack: 'Slack', discord: 'Discord' };

  $effect(() => clock.start());

  $effect(() =>
    events.on('pairing.linked', (e) => {
      const d = e.data as PairingLinkedData;
      if (!channel || d.channel === channel) linked = d;
    }),
  );

  async function create() {
    busy = true;
    error = '';
    linked = null;
    try {
      code = await api.post<PairingCode>('/api/pairing');
    } catch (e) {
      error = errorText(e);
    } finally {
      busy = false;
    }
  }

  $effect(() => {
    if (auto && !code && !busy && !error) void create();
  });
</script>

{#if linked}
  <StatusLine tone="success" text="Linked as {linked.display}." />
{:else if error}
  <StatusLine tone="danger" text="Bothy couldn't make a code. Try again." details={error} action={{ label: 'Try again', onclick: create }} />
{:else if !code}
  {#if !auto}<Button loading={busy} loadingLabel="Making a code…" onclick={create}>Link your account</Button>{/if}
{:else if left === 'Expired'}
  <p class="text-body text-muted">This code expired. <LinkButton onclick={create}>Make a new code</LinkButton></p>
{:else}
  <div class="flex flex-wrap items-start gap-x-6 gap-y-4">
    {#if link && channel !== 'slack'}<QRCode text={link[1]} label="QR code that opens the bot in {apps[link[0]] ?? link[0]} and links your account" />{/if}
    <!-- The basis makes this column drop under the QR code on phones instead of squeezing beside it. -->
    <div class="min-w-0 max-w-md flex-1 basis-56 space-y-3">
      {#if link}<Button href={link[1]} external>Open {apps[link[0]] ?? link[0]}</Button>{/if}
      <p class="text-body text-text">
        {channel === 'slack' ? `Send this to ${bot} in Slack:` : channel === 'discord' ? 'DM the bot:' : link ? 'Or send this to the bot:' : `Send this to ${bot} in a direct message:`}
      </p>
      <CommandBlock code={command} />
      <p class="text-small text-muted">{left}.</p>
    </div>
  </div>
{/if}
