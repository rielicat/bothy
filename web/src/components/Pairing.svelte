<script lang="ts">
  import { api } from '../lib/api/client';
  import type { PairingCode, PairingLinkedData } from '../lib/api/types.gen';
  import { countdown } from '../lib/format';
  import { channelName } from '../lib/labels';
  import { events } from '../lib/sse.svelte';
  import { clock } from '../lib/stores/clock.svelte';
  import { toasts } from '../lib/stores/ui.svelte';
  import Icon from './Icon.svelte';
  import QRCode from './QRCode.svelte';

  let { channel = '' }: { channel?: string } = $props();

  let code = $state<PairingCode | null>(null);
  let linked = $state<PairingLinkedData | null>(null);
  let busy = $state(false);

  const pretty = $derived(code ? `${code.code.slice(0, 4)}-${code.code.slice(4)}` : '');
  const links = $derived(Object.entries(code?.deep_links ?? {}).filter(([c, u]) => (!channel || c === channel) && /^https:\/\//.test(u)));
  const left = $derived(code ? countdown(code.expires_at, clock.now) : '');

  $effect(() =>
    events.on('pairing.linked', (e) => {
      const d = e.data as PairingLinkedData;
      if (!channel || d.channel === channel) linked = d;
    }),
  );

  async function create() {
    busy = true;
    linked = null;
    try {
      code = await api.post<PairingCode>('/api/pairing');
    } catch (e) {
      toasts.error(e);
    } finally {
      busy = false;
    }
  }
</script>

{#if linked}
  <p class="alert-ok" role="status"><Icon name="check" size={16} />Linked {channelName[linked.channel] ?? linked.channel} account {linked.display}. Bothy just said hello there.</p>
{:else if !code || left === '0:00'}
  <button type="button" class="btn" disabled={busy} onclick={create}><Icon name="qr" size={16} />{code ? 'New pairing code' : 'Link your account'}</button>
{:else}
  <div class="flex flex-wrap items-center gap-5" aria-live="polite">
    {#each links.slice(0, 1) as [c, url] (c)}
      <QRCode text={url} label="QR code that opens {channelName[c] ?? c} and links your account" />
    {/each}
    <div class="space-y-2 text-sm">
      {#each links as [c, url] (c)}
        <a class="btn btn-primary" href={url} target="_blank" rel="noopener noreferrer">Open {channelName[c] ?? c}<Icon name="external" size={14} /></a>
      {/each}
      <p>Or send <code class="rounded bg-sunken px-1.5 py-0.5 font-mono font-semibold tracking-wider">/pair {pretty}</code> to the bot in a direct message.</p>
      <p class="text-xs text-muted">Waiting for you… the code expires in {left}.</p>
    </div>
  </div>
{/if}
