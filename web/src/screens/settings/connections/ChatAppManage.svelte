<script lang="ts">
  import { Button, Dialog, IconButton, ListRow, Menu, Select, SettingsRow, SettingsSection, StatusLine, Switch, toast } from '../../../components/ui';
  import { api } from '../../../lib/api/client';
  import type { ChannelIdentity, ChannelTestResp, SecretMeta, Space, User } from '../../../lib/api/types.gen';
  import { config } from '../../../lib/config.svelte';
  import { relTime } from '../../../lib/format';
  import { session } from '../../../lib/stores/session.svelte';
  import ConnPage from './ConnPage.svelte';
  import { appStatus, appTitle, channelFailure, connectedText, failureCode, saveConfig, secretName, type AppName, type AppStatus, type Failure } from './connections';

  let {
    app,
    status,
    identities,
    spaces,
    users,
    secrets,
    onchanged,
    onreplace,
  }: {
    app: AppName;
    status: AppStatus;
    identities: ChannelIdentity[];
    spaces: Space[];
    users: User[];
    secrets: SecretMeta[];
    onchanged: () => void;
    /** Back to the steps to paste new tokens, keeping the failure that sent the person there. */
    onreplace: (failure: Failure) => void;
  } = $props();

  const name = $derived(appTitle[app]);
  let on = $state(false);
  let onStatus = $state<'saving' | 'saved' | null>(null);
  let onError = $state('');
  let testing = $state(false);
  let test = $state<(Failure & { ok: boolean; rejected?: boolean }) | null>(null);
  let leaving = $state(false);
  let busy = $state(false);
  let unknown = $state('pair');
  let unknownStatus = $state<'saving' | 'saved' | null>(null);
  let unknownError = $state('');
  let linking = $state<ChannelIdentity | null>(null);
  let linkTo = $state('');
  let linkError = $state('');
  let groupError = $state('');

  $effect.pre(() => {
    on = status.enabled;
    unknown = config.value<string>('channels.unknown_senders', 'pair');
  });

  const groups = $derived(spaces.filter((s) => s.chat.channel === app));
  const mine = $derived(identities.filter((i) => i.channel === app));
  const linked = $derived(mine.filter((i) => i.status === 'linked' || i.status === 'guest'));
  const waiting = $derived(mine.filter((i) => i.status === 'pending'));
  const person = (id?: string) => users.find((u) => u.id === id);
  const handle = (i: ChannelIdentity) => (i.username ? `@${i.username}` : i.display_name);
  const line = $derived(status.enabled && status.state !== 'connected' ? appStatus(app, status, 0, true) : null);
  const isOwner = $derived(session.me?.user.role === 'owner');
  // Linking gives the chat account that person's chats and permissions. Only
  // the owner may link it to someone else, and must pick them explicitly.
  const people = $derived(isOwner ? users.filter((u) => !u.disabled) : users.filter((u) => u.id === session.me?.user.id));

  async function setOn(v: boolean) {
    onStatus = 'saving';
    onError = '';
    const bad = await saveConfig({ [`channels.${app}.enabled`]: v });
    onStatus = bad ? null : 'saved';
    if (bad) {
      on = !v;
      onError = bad.message;
    } else onchanged();
  }

  async function runTest() {
    testing = true;
    test = null;
    try {
      const r = await api.post<ChannelTestResp>(`/api/channels/${app}/test`);
      test = { ok: true, message: `The connection works. ${name} knows Bothy as ${r.identity}.` };
    } catch (e) {
      test = { ok: false, rejected: failureCode(e) === 'key_rejected', ...channelFailure(app, e) };
    } finally {
      testing = false;
    }
  }

  async function disconnect() {
    busy = true;
    const bad = await saveConfig({ [`channels.${app}.enabled`]: false });
    const keys: Record<AppName, [string, string][]> = {
      slack: [
        ['app_token', 'slack_app_token'],
        ['bot_token', 'slack_bot_token'],
      ],
      telegram: [['token', 'telegram_bot_token']],
      discord: [['token', 'discord_bot_token']],
    };
    if (!bad)
      for (const [k, d] of keys[app]) {
        const n = secretName(`channels.${app}.${k}`, d);
        if (secrets.some((s) => s.name === n && s.source === 'vault')) await api.del(`/api/vault/global/${n}`).catch(() => {});
      }
    busy = false;
    leaving = false;
    if (bad) onError = bad.message;
    else {
      toast(`${name} disconnected.`);
      onchanged();
    }
  }

  // A refused change leaves the bound value as it was, so the control the
  // person just changed is put back by hand.
  async function patchSpace(s: Space, body: Record<string, unknown>, undo: () => void) {
    groupError = '';
    try {
      Object.assign(s, await api.patch<Space>(`/api/spaces/${encodeURIComponent(s.principal_id)}`, body));
    } catch {
      undo();
      groupError = "Couldn't save. Try again.";
    }
  }

  async function setUnknown(v: string) {
    unknownStatus = 'saving';
    unknownError = '';
    const bad = await saveConfig({ 'channels.unknown_senders': v });
    unknownStatus = bad ? null : 'saved';
    if (bad) unknownError = bad.message;
  }

  const idPath = (i: ChannelIdentity) => `/api/identities/${encodeURIComponent(i.channel)}/${encodeURIComponent(i.external_id)}`;

  async function decide(i: ChannelIdentity, body: Record<string, string>, done: string) {
    try {
      await api.patch(idPath(i), body);
      toast(done);
      linking = null;
      onchanged();
    } catch {
      linkError = "Couldn't save. Try again.";
    }
  }

  function openLink(i: ChannelIdentity) {
    linkError = '';
    linkTo = isOwner ? '' : (session.me?.user.id ?? '');
    linking = i;
  }

  function link() {
    if (!linking) return;
    if (!linkTo) {
      linkError = 'Choose a person.';
      return;
    }
    void decide(linking, { status: 'linked', user_id: linkTo }, 'Account linked.');
  }
</script>

<ConnPage title={name}>
  <SettingsSection title="Connection">
    <div class="flex min-h-14 flex-wrap items-center justify-between gap-x-6 gap-y-2 border-b border-border py-3">
      <p class="min-w-0 text-body {line ? (line.tone === 'danger' ? 'text-danger-text' : 'text-text') : 'text-text'}">
        {!status.enabled ? `Turned off. Bothy doesn't answer in ${name}.` : line ? line.text : connectedText(status, true)}
      </p>
      <Button size="sm" loading={testing} loadingLabel="Testing…" onclick={runTest}>Test connection</Button>
    </div>
    {#if test}
      <StatusLine
        class="border-b border-border py-3"
        tone={test.ok ? 'success' : 'danger'}
        text={test.message}
        details={test.details}
        action={test.rejected ? { label: app === 'slack' ? 'Replace the tokens' : 'Replace the token', onclick: () => onreplace(test!) } : undefined}
        alert
      />
    {/if}
    <SettingsRow label="Turned on" inline status={onStatus} error={onError}>
      {#snippet children(c)}<Switch id={c.id} label="Turned on" hideLabel bind:checked={on} onchange={setOn} />{/snippet}
    </SettingsRow>
    <div class="pt-3">
      <Button variant="danger-text" class="-ml-4" onclick={() => (leaving = true)}>Disconnect {name}…</Button>
    </div>
  </SettingsSection>

  <SettingsSection title="Group chats">
    {#each groups as s (s.principal_id)}
      {@const profile = session.principals.find((p) => p.id === s.principal_id)?.tool_profile ?? 'restricted'}
      <div class="border-b border-border py-3 last:border-b-0">
        <div class="flex min-h-8 flex-wrap items-center justify-between gap-x-4 gap-y-1">
          <span class="text-body font-medium text-text" data-copy-ignore>{s.title}</span>
          <Switch label="Answers here" checked={s.enabled} onchange={(v) => patchSpace(s, { enabled: v }, () => (s.enabled = !v))} />
        </div>
        <div class="mt-3 grid gap-3 sm:grid-cols-2">
          <Select
            label="Answers when"
            value={s.mention_only ? 'mention' : 'any'}
            options={[
              { value: 'mention', label: 'Mentioned' },
              { value: 'any', label: 'Any message' },
            ]}
            onchange={(e) => {
              const el = e.currentTarget;
              void patchSpace(s, { mention_only: el.value === 'mention' }, () => (el.value = s.mention_only ? 'mention' : 'any'));
            }}
          />
          <Select
            label="What Bothy can do here"
            value={profile === 'standard' ? 'standard' : 'restricted'}
            options={[
              { value: 'restricted', label: 'Read only' },
              { value: 'standard', label: 'Standard' },
            ]}
            onchange={(e) => {
              const el = e.currentTarget;
              void patchSpace(s, { tool_profile: el.value }, () => (el.value = profile));
            }}
          />
        </div>
      </div>
    {:else}
      <p class="text-body text-muted">Bothy isn't in any {name} group chats yet. Add it to one and it shows up here.</p>
    {/each}
    {#if groupError}<StatusLine class="mt-2" tone="danger" alert text={groupError} />{/if}
  </SettingsSection>

  <SettingsSection title="Linked people">
    {#each linked as i (i.external_id)}
      <ListRow title="{person(i.user_id)?.display_name ?? i.display_name} · {handle(i)}{i.status === 'guest' ? ' · Guest' : ''}" />
    {:else}
      <p class="text-body text-muted">Nobody has linked a {name} account yet. People link their own from Your account.</p>
    {/each}
  </SettingsSection>

  <SettingsSection title="People who aren't linked">
    <SettingsRow label="When someone without an account messages Bothy" status={unknownStatus} error={unknownError}>
      {#snippet children(c)}
        <Select
          id={c.id}
          aria-describedby={c.describedBy}
          bind:value={unknown}
          onchange={() => setUnknown(unknown)}
          options={[
            { value: 'pair', label: 'Ask an admin first (recommended)' },
            { value: 'ignore', label: 'Ignore them' },
          ]}
        />
      {/snippet}
    </SettingsRow>
    {#if waiting.length}
      <h3 class="mt-6 text-body font-medium text-text">Waiting for an admin</h3>
      <div class="mt-1">
        {#each waiting as i (i.external_id)}
          <ListRow title="{i.display_name || i.username} · {handle(i)}" meta={[relTime(i.first_seen_at)]}>
            {#snippet trailing()}
              <Menu
                placement="bottom-end"
                items={[
                  { label: 'Link to a person…', onselect: () => openLink(i) },
                  { label: 'Allow as a guest', onselect: () => decide(i, { status: 'guest' }, 'Added as a guest.') },
                  { label: 'Block', danger: true, onselect: () => decide(i, { status: 'blocked' }, 'Blocked.') },
                ]}
              >
                {#snippet trigger(props)}<IconButton icon="more" label="Options for {handle(i)}" size="sm" variant="quiet" {...props} />{/snippet}
              </Menu>
            {/snippet}
          </ListRow>
        {/each}
      </div>
    {/if}
  </SettingsSection>
</ConnPage>

<Dialog bind:open={leaving} title="Disconnect {name}?" initialFocus="cancel" {busy}>
  <p class="text-body text-text">Bothy stops answering in {name} and forgets its tokens. Linked accounts and group chat settings are kept.</p>
  {#snippet footer({ close })}
    <Button data-cancel onclick={close}>Cancel</Button>
    <Button variant="danger" loading={busy} loadingLabel="Disconnecting…" onclick={disconnect}>Disconnect {name}</Button>
  {/snippet}
</Dialog>

<Dialog open={!!linking} onclose={() => (linking = null)} title="Link {linking ? handle(linking) : ''} to a person">
  <div class="space-y-4">
    <p class="text-body text-text">Messages from {linking ? handle(linking) : ''} in {name} will act as this person, with their chats and permissions.</p>
    <Select
      label="Person"
      bind:value={linkTo}
      error={linkError}
      options={[...(isOwner ? [{ value: '', label: 'Choose a person' }] : []), ...people.map((u) => ({ value: u.id, label: u.display_name || u.username }))]}
    />
  </div>
  {#snippet footer({ close })}
    <Button data-cancel onclick={close}>Cancel</Button>
    <Button variant="primary" onclick={link}>Link account</Button>
  {/snippet}
</Dialog>
