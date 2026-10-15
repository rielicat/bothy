<script lang="ts">
  import { untrack } from 'svelte';
  import Pairing from '../../components/Pairing.svelte';
  import { Button, Combobox, Dialog, LinkButton, Segmented, Select, SettingsRow, SettingsSection, StatusLine, TextField, toast } from '../../components/ui';
  import { api, ApiError } from '../../lib/api/client';
  import type { ChannelIdentity, ChannelStatus, Profile, TokenRecord } from '../../lib/api/types.gen';
  import { isZeroTime, relTime } from '../../lib/format';
  import { localZone, zoneLabel, zoneOptions } from '../../lib/schedules';
  import { events } from '../../lib/sse.svelte';
  import { session } from '../../lib/stores/session.svelte';
  import { theme, type ThemePref } from '../../lib/stores/ui.svelte';
  import ConfirmDialog from './ConfirmDialog.svelte';
  import ItemRow from './ItemRow.svelte';
  import PasswordDialog from './PasswordDialog.svelte';
  import { expired, lastUsed, scopeSummary } from './accountHelpers';
  import { Saver } from './store.svelte';
  import SettingsFrame from './SettingsFrame.svelte';
  import TokenDialog from './TokenDialog.svelte';

  const apps: Record<string, string> = { slack: 'Slack', telegram: 'Telegram', discord: 'Discord' };
  const appOrder = Object.keys(apps);

  const me = $derived(session.me?.user);

  let profile = $state<Profile | null>(null);
  let identities = $state<ChannelIdentity[]>([]);
  let channels = $state<ChannelStatus[] | null>(null);
  let tokens = $state<TokenRecord[]>([]);
  let tokensFailed = $state(false);

  let pwOpen = $state(false);
  let tokenOpen = $state(false);
  let pairOpen = $state(false);
  let pairChannel = $state('');
  let revoking = $state<TokenRecord | null>(null);
  let revokeBusy = $state(false);
  let revokeError = $state('');

  async function loadIdentities() {
    const all = await api.get<ChannelIdentity[]>('/api/identities').catch(() => identities);
    identities = all.filter((i) => i.status === 'linked' && i.user_id === me?.id);
  }

  // Linking or unlinking an app changes where approval requests can go.
  async function loadProfile() {
    profile = await api.get<Profile>('/api/me').catch(() => profile);
  }

  async function loadTokens() {
    try {
      tokens = await api.get<TokenRecord[]>('/api/tokens');
      tokensFailed = false;
    } catch {
      tokensFailed = true;
    }
  }

  $effect(() => {
    untrack(() => {
      void loadIdentities();
      void loadTokens();
      void loadProfile();
      // Members can't list chat apps yet, so they see only the apps they've linked.
      if (session.isAdmin) api.get<ChannelStatus[]>('/api/channels').then((c) => (channels = c), () => (channels = []));
    });
    return events.on('pairing.linked', () => {
      void loadIdentities();
      void loadProfile();
    });
  });

  const nameSaver = new Saver();
  let name = $state('');
  let nameBase = '';
  $effect.pre(() => {
    const n = me?.display_name ?? '';
    untrack(() => {
      if (name === nameBase) name = n;
      nameBase = n;
    });
  });

  async function saveName() {
    const n = name.trim();
    if (!n) return nameSaver.fail('Enter your name.');
    if (n === nameBase || !me) return nameSaver.clear();
    const ok = await nameSaver.run(async () => (profile = await api.patch<Profile>('/api/me', { display_name: n })));
    // The server refuses a name someone else here has, hidden characters and more than 64 characters.
    if (!ok && nameSaver.lastError instanceof ApiError && nameSaver.lastError.status === 400)
      nameSaver.fail("Bothy can't use that name. Choose one nobody else here has, up to 64 characters.");
    if (ok) {
      nameBase = n;
      await session.refresh().catch(() => {});
    }
  }

  const zoneSaver = new Saver();
  const zone = $derived(profile?.timezone || me?.timezone || '');
  const zones = $derived(zoneOptions(zone));
  const browserZone = localZone();
  let zoneValue = $state('');
  $effect.pre(() => {
    zoneValue = zone;
  });

  async function saveZone(v: string | string[]) {
    if (typeof v !== 'string' || !v || v === zone) return;
    zoneValue = v;
    const ok = await zoneSaver.run(async () => (profile = await api.patch<Profile>('/api/me', { timezone: v })));
    if (!ok) {
      zoneValue = zone;
      if (zoneSaver.lastError instanceof ApiError && zoneSaver.lastError.status === 400) zoneSaver.fail("Bothy doesn't know that time zone. Choose one from the list.");
      return;
    }
    // New schedules start in the zone session.me carries.
    await session.refresh().catch(() => {});
  }

  const linkedApps = $derived(appOrder.filter((c) => identities.some((i) => i.channel === c)));
  const appRows = $derived(
    appOrder
      .filter((c) => linkedApps.includes(c) || channels?.some((s) => s.name === c && s.enabled && s.state !== 'disabled'))
      .map((c) => ({ channel: c, label: apps[c], identity: identities.find((i) => i.channel === c) })),
  );

  const approvalsSaver = new Saver();
  // The apps the server lets requests go to: the ones linked with a chat Bothy can write to.
  const approvalApps = $derived(appOrder.filter((c) => profile?.chats.includes(c)));
  const approvalOptions = $derived([{ value: 'web', label: 'Bothy only' }, ...approvalApps.map((c) => ({ value: c, label: apps[c] }))]);

  async function saveApprovals(v: string) {
    const was = profile;
    if (!was) return;
    profile = { ...was, approvals_in: v };
    if (!(await approvalsSaver.run(async () => (profile = await api.patch<Profile>('/api/me', { approvals_in: v }))))) profile = was;
  }

  let unlinkError = $state('');

  async function unlink(i: ChannelIdentity) {
    unlinkError = '';
    try {
      await api.del(`/api/identities/${encodeURIComponent(i.channel)}/${encodeURIComponent(i.external_id)}`);
      identities = identities.filter((x) => x !== i);
      void loadProfile();
      toast(`${apps[i.channel] ?? 'App'} unlinked.`);
    } catch {
      unlinkError = `Bothy couldn't unlink ${apps[i.channel] ?? 'the app'}. Try again.`;
    }
  }

  function link(channel: string) {
    pairChannel = channel;
    pairOpen = true;
  }

  async function revoke() {
    const t = revoking;
    if (!t) return;
    revokeBusy = true;
    revokeError = '';
    try {
      await api.del(`/api/tokens/${encodeURIComponent(t.id)}`);
      tokens = tokens.filter((x) => x.id !== t.id);
      revokeBusy = false;
      revoking = null;
      toast('Token revoked.');
    } catch (e) {
      revokeBusy = false;
      revokeError = e instanceof ApiError && e.status === 404 ? 'This token was already revoked.' : "Bothy couldn't revoke the token. Try again.";
    }
  }

  const activeTokens = $derived(tokens.filter((t) => isZeroTime(t.revoked_at)));
  const themes = [
    { value: 'system', label: 'System' },
    { value: 'light', label: 'Light' },
    { value: 'dark', label: 'Dark' },
  ];
</script>

{#snippet browserZoneNote()}
  This browser uses {zoneLabel(browserZone)}. <LinkButton aria-label="Use {zoneLabel(browserZone)}" onclick={() => saveZone(browserZone)}>Use it</LinkButton>
{/snippet}

<SettingsFrame title="Account">
  <SettingsSection title="Profile">
    <SettingsRow label="Name" status={nameSaver.status} error={nameSaver.error}>
      {#snippet children(c)}
        <TextField
          id={c.id}
          bind:value={name}
          autocomplete="name"
          aria-describedby={c.describedBy}
          aria-invalid={c.invalid || undefined}
          onblur={saveName}
          onkeydown={(e: KeyboardEvent) => {
            if (e.key === 'Enter') {
              e.preventDefault();
              void saveName();
            }
          }}
        />
      {/snippet}
    </SettingsRow>
    <SettingsRow label="Username">
      {#snippet children(c)}<output id={c.id} class="block text-body break-words text-text">{me?.username}</output>{/snippet}
    </SettingsRow>
    <SettingsRow
      label="Time zone"
      help="Bothy uses it in your chats and for new schedules."
      note={zone && browserZone !== zone ? browserZoneNote : undefined}
      status={zoneSaver.status}
      error={zoneSaver.error}
    >
      {#snippet children()}
        <!-- The row's label is the visible one; the combobox keeps its own for screen readers. -->
        <Combobox label="Time zone" class="[&>div]:mt-0 [&>label]:sr-only" bind:value={zoneValue} options={zones} onchange={saveZone} />
      {/snippet}
    </SettingsRow>
    <SettingsRow label="Password">
      {#snippet children()}<Button onclick={() => (pwOpen = true)}>Change password…</Button>{/snippet}
    </SettingsRow>
  </SettingsSection>

  <SettingsSection title="Appearance">
    <SettingsRow label="Theme">
      {#snippet children()}<Segmented label="Theme" options={themes} value={theme.pref} onchange={(v) => theme.set(v as ThemePref)} />{/snippet}
    </SettingsRow>
  </SettingsSection>

  {#if profile && approvalApps.length}
    <SettingsSection title="Approvals">
      <SettingsRow label="Requests reach you in" help="Bothy also shows them in Approvals." status={approvalsSaver.status} error={approvalsSaver.error}>
        {#snippet children(c)}
          <Select
            id={c.id}
            value={profile?.approvals_in || 'web'}
            options={approvalOptions}
            aria-describedby={c.describedBy}
            onchange={(e: Event) => saveApprovals((e.currentTarget as HTMLSelectElement).value)}
          />
        {/snippet}
      </SettingsRow>
    </SettingsSection>
  {/if}

  <SettingsSection title="Linked chat apps">
    {#if appRows.length}
      <div>
        {#each appRows as r (r.channel)}
          <ItemRow name={r.label} status={r.identity ? (r.identity.username ? `@${r.identity.username}` : r.identity.display_name) : 'Not linked'}>
            {#snippet trailing()}
              {#if r.identity}
                {@const i = r.identity}
                <Button size="sm" aria-label="Unlink {r.label}" onclick={() => unlink(i)}>Unlink</Button>
              {:else}
                <Button size="sm" aria-label="Link {r.label}" onclick={() => link(r.channel)}>Link</Button>
              {/if}
            {/snippet}
          </ItemRow>
        {/each}
      </div>
      {#if unlinkError}<StatusLine class="mt-2" tone="danger" text={unlinkError} alert />{/if}
    {:else if session.isAdmin && channels !== null}
      <p class="py-2 text-body text-muted">No chat apps are connected yet.</p>
      <Button class="mt-2" href="/settings/connections">Connect a chat app</Button>
    {:else if !session.isAdmin}
      <p class="py-2 text-body text-muted">Link Slack, Telegram or Discord to talk to Bothy there.</p>
      <Button class="mt-2" onclick={() => link('')}>Link a chat app</Button>
    {/if}
  </SettingsSection>

  <SettingsSection title="Saved keys">
    <p class="py-2 text-body text-muted">Your own keys for connected tools, used instead of the shared ones when Bothy works for you.</p>
    <Button class="mt-2" href="/settings/account/keys">Manage your keys</Button>
  </SettingsSection>

  <SettingsSection title="API access">
    {#if tokensFailed}
      <StatusLine tone="danger" text="Your tokens didn't load. Check your connection and try again." action={{ label: 'Try again', onclick: loadTokens }} />
    {:else}
      {#if activeTokens.length}
        <div>
          {#each activeTokens as t (t.id)}
            {@const old = expired(t)}
            <ItemRow name={t.name} stacked tone={old ? 'warning' : 'muted'} status="{scopeSummary(t.scopes)} · {old ? `Expired ${relTime(t.expires_at)}` : lastUsed(t)}">
              {#snippet trailing()}<Button size="sm" aria-label="Revoke {t.name}…" onclick={() => (revoking = t)}>Revoke…</Button>{/snippet}
            </ItemRow>
          {/each}
        </div>
      {:else}
        <p class="py-2 text-body text-muted">No tokens yet. A token lets a script or another app use Bothy as you.</p>
      {/if}
      <Button class="mt-4" onclick={() => (tokenOpen = true)}>Create token</Button>
    {/if}
  </SettingsSection>

  <div class="mt-10">
    <Button onclick={() => session.logout()}>Sign out</Button>
  </div>
</SettingsFrame>

<PasswordDialog bind:open={pwOpen} />
<TokenDialog bind:open={tokenOpen} oncreated={(t) => (tokens = [...tokens, t])} />

<Dialog bind:open={pairOpen} title={pairChannel ? `Link ${apps[pairChannel]}` : 'Link a chat app'} onclose={loadIdentities}>
  <Pairing channel={pairChannel} auto />
  {#snippet footer({ close })}
    <Button onclick={close}>Done</Button>
  {/snippet}
</Dialog>

<ConfirmDialog
  open={!!revoking}
  title="Revoke {revoking?.name}?"
  confirm="Revoke token"
  busyLabel="Revoking…"
  busy={revokeBusy}
  error={revokeError}
  onconfirm={revoke}
  onclose={() => {
    revoking = null;
    revokeError = '';
  }}>Apps using this token stop working right away. This can't be undone.</ConfirmDialog
>
