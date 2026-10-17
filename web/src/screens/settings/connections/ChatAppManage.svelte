<script lang="ts">
  import { Button, Dialog, Select, SettingsRow, SettingsSection, StatusLine, Switch, toast } from '../../../components/ui';
  import { api } from '../../../lib/api/client';
  import type { ChannelTestResp, SecretMeta, SpaceView } from '../../../lib/api/types.gen';
  import { config } from '../../../lib/config.svelte';
  import { plural } from '../../../lib/format';
  import ConfirmDialog from '../ConfirmDialog.svelte';
  import { pictureDownloadURL, pictureURL } from '../picture';
  import SlackPictureNote from '../SlackPictureNote.svelte';
  import ConnPage from './ConnPage.svelte';
  import { appStatus, appTitle, channelFailure, connectedText, failureCode, saveConfig, secretName, type AppName, type AppStatus, type Failure } from './connections';

  let {
    app,
    status,
    spaces,
    secrets,
    onchanged,
    onreplace,
  }: {
    app: AppName;
    status: AppStatus;
    spaces: SpaceView[];
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
  let groupError = $state('');
  let turningOff = $state<SpaceView | null>(null);
  let deleting = $state<SpaceView | null>(null);
  let deleteBusy = $state(false);
  let deleteError = $state('');

  $effect.pre(() => {
    on = status.enabled;
    unknown = config.value<string>('channels.unknown_senders', 'pair');
  });

  const groups = $derived(spaces.filter((s) => s.chat.channel === app));
  const line = $derived(status.enabled && status.state !== 'connected' ? appStatus(app, status, 0, true) : null);
  const tools = [
    { value: 'restricted', label: 'Answer questions' },
    { value: 'standard', label: 'Do work, with approval' },
  ];

  const groupMeta = (s: SpaceView) =>
    [s.team_name, s.members > 0 ? plural(s.members, 'person', 'people') : '', s.external ? 'Shared with other companies' : ''].filter(Boolean).join(' · ');

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
  async function patchSpace(s: SpaceView, body: Record<string, unknown>, undo: () => void) {
    groupError = '';
    try {
      Object.assign(s, await api.patch<SpaceView>(`/api/spaces/${encodeURIComponent(s.principal_id)}`, body));
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

  // People Discord admits only through this server lose access with its last channel.
  const lastInServer = (s: SpaceView) => app === 'discord' && !!s.team_id && !groups.some((g) => g !== s && g.enabled && g.team_id === s.team_id);

  function setAnswers(s: SpaceView, v: boolean) {
    if (!v && lastInServer(s)) turningOff = s;
    else void patchSpace(s, { enabled: v }, () => (s.enabled = !v));
  }

  function keepOn() {
    if (turningOff) turningOff.enabled = true;
    turningOff = null;
  }

  function turnOff() {
    const s = turningOff;
    turningOff = null;
    if (s) void patchSpace(s, { enabled: false }, () => (s.enabled = true));
  }

  async function deleteGroup() {
    const s = deleting;
    if (!s) return;
    deleteBusy = true;
    deleteError = '';
    try {
      await api.del(`/api/spaces/${encodeURIComponent(s.principal_id)}`);
      deleting = null;
      toast('Group chat deleted.');
      onchanged();
    } catch {
      deleteError = "Bothy couldn't delete it. Try again.";
    } finally {
      deleteBusy = false;
    }
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
    {#if app === 'slack'}
      <SettingsRow label="Picture">
        {#snippet note()}<SlackPictureNote appId={status.app_id} download={false} />{/snippet}
        {#snippet children(c)}
          <div class="flex items-center gap-3">
            <img src={pictureURL()} alt={config.value<string>('agent.name', 'Bothy')} width="32" height="32" class="size-8 shrink-0 rounded-md border border-border bg-subtle" />
            <Button size="sm" href={pictureDownloadURL} download aria-describedby={c.labelId}>Download picture</Button>
          </div>
        {/snippet}
      </SettingsRow>
    {/if}
    <div class="pt-3">
      <Button variant="danger-text" class="-ml-4" onclick={() => (leaving = true)}>Disconnect {name}…</Button>
    </div>
  </SettingsSection>

  <SettingsSection title="Group chats">
    {#each groups as s (s.principal_id)}
      {@const meta = groupMeta(s)}
      <div class="border-b border-border py-3 last:border-b-0">
        <div class="flex min-h-8 flex-wrap items-center justify-between gap-x-4 gap-y-1">
          <span class="min-w-0">
            <span class="block text-body font-medium text-text" data-copy-ignore>{s.title}</span>
            {#if meta}<span class="block text-small text-muted tabular-nums" data-copy-ignore>{meta}</span>{/if}
          </span>
          <span class="flex items-center gap-2">
            <Switch label="Answers here" bind:checked={s.enabled} onchange={(v) => setAnswers(s, v)} />
            <!-- Only a channel that is off can go, so nobody deletes one people are using. -->
            {#if !s.enabled}<Button size="sm" variant="ghost" aria-label="Delete {s.title}…" onclick={() => (deleting = s)}>Delete…</Button>{/if}
          </span>
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
            value={s.tool_profile === 'standard' ? 'standard' : 'restricted'}
            options={tools}
            onchange={(e) => {
              const el = e.currentTarget;
              const was = s.tool_profile === 'standard' ? 'standard' : 'restricted';
              void patchSpace(s, { tool_profile: el.value }, () => (el.value = was));
            }}
          />
        </div>
      </div>
    {:else}
      <p class="text-body text-muted">Bothy isn't in any {name} group chats yet. Add it to one and it shows up here.</p>
    {/each}
    {#if groupError}<StatusLine class="mt-2" tone="danger" alert text={groupError} />{/if}
  </SettingsSection>

  <SettingsSection title="Who can use Bothy">
    {#if app === 'slack'}
      <p class="text-body text-text">
        {status.org ? 'Everyone in your Slack organization, across all its workspaces,' : `Everyone in ${status.team || 'your workspace'}`} can message Bothy and use what it can
        read, such as your repositories and connected tools. Guests and people from other companies can't. Anything that changes something waits for an approver.
      </p>
    {:else if app === 'discord'}
      <p class="text-body text-text">
        {#if status.member_check === 'no_access'}
          Bothy can't check who's in your servers, so only people you let in from Approvals can message it.
        {:else}
          Members of a server where Bothy answers in a channel can message Bothy and use what it can read, such as your repositories and connected tools. Bots can't. Anything
          that changes something waits for an approver.
        {/if}
      </p>
      {#if status.idle_servers}
        <p class="mt-2 text-body text-muted">Bothy is in {plural(status.idle_servers, 'server')} where it isn't on in any channel. Remove it from servers you don't use.</p>
      {/if}
    {:else}
      <p class="text-body text-text">People you let in from Approvals.</p>
    {/if}
    {#if app !== 'slack'}
      <SettingsRow
        label="When someone new messages Bothy"
        help="Either way, they're listed in Approvals. Applies to Telegram and Discord."
        status={unknownStatus}
        error={unknownError}
      >
        {#snippet children(c)}
          <Select
            id={c.id}
            aria-describedby={c.describedBy}
            bind:value={unknown}
            onchange={() => setUnknown(unknown)}
            options={[
              { value: 'pair', label: 'Ask an admin (recommended)' },
              { value: 'ignore', label: "Don't reply or notify anyone" },
            ]}
          />
        {/snippet}
      </SettingsRow>
    {/if}
    <Button class="mt-4" href="/settings/people">Manage people</Button>
  </SettingsSection>
</ConnPage>

<Dialog bind:open={leaving} title="Disconnect {name}?" initialFocus="cancel" {busy}>
  <p class="text-body text-text">Bothy stops answering in {name} and forgets its tokens. People and group chat settings are kept.</p>
  {#snippet footer({ close })}
    <Button data-cancel onclick={close}>Cancel</Button>
    <Button variant="danger" loading={busy} loadingLabel="Disconnecting…" onclick={disconnect}>Disconnect {name}</Button>
  {/snippet}
</Dialog>

<ConfirmDialog open={!!turningOff} title="Turn off Bothy in {turningOff?.title}?" confirm="Turn off" onconfirm={turnOff} onclose={keepOn}>
  People who are only in {turningOff?.team_name || 'the Discord server'} won't be able to message Bothy any more.
</ConfirmDialog>

<ConfirmDialog
  open={!!deleting}
  title="Delete {deleting?.title}?"
  confirm="Delete group chat"
  busyLabel="Deleting…"
  busy={deleteBusy}
  error={deleteError}
  onconfirm={deleteGroup}
  onclose={() => {
    deleting = null;
    deleteError = '';
  }}
  >This deletes its chats, files, notes and schedules in Bothy, and what Bothy may do there without asking. If Bothy is still in {deleting?.title}, the next message there brings it back to Approvals. This can't be undone.</ConfirmDialog
>
