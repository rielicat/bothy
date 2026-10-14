<script lang="ts">
  import { untrack, type Snippet } from 'svelte';
  import Pairing from '../../../components/Pairing.svelte';
  import { Button, EmptyState, LinkButton, StatusLine, TextField } from '../../../components/ui';
  import { api } from '../../../lib/api/client';
  import type { ChannelIdentity, ChannelTestResp, DiscordInvite, SecretMeta, Space, User } from '../../../lib/api/types.gen';
  import { config } from '../../../lib/config.svelte';
  import ChatAppManage from './ChatAppManage.svelte';
  import ConnPage from './ConnPage.svelte';
  import { appTitle, channelFailure, connectedText, isApp, saveConfig, secretName, type AppName, type AppStatus, type Failure } from './connections';

  let { params }: { params: Record<string, string> } = $props();
  const app = $derived((isApp(params.item) ? params.item : 'slack') as AppName);
  const name = $derived(appTitle[app]);

  type Field = { key: string; label: string; help: string; secret?: string; re: RegExp };
  const fields: Record<AppName, Field[]> = {
    slack: [
      { key: 'app_token', label: 'App-level token', help: 'Starts with xapp-. In Slack: Basic Information → App-Level Tokens.', secret: 'slack_app_token', re: /^xapp-\S{16,}$/ },
      { key: 'bot_token', label: 'Bot token', help: 'Starts with xoxb-. In Slack: OAuth & Permissions.', secret: 'slack_bot_token', re: /^xoxb-\S{16,}$/ },
    ],
    telegram: [{ key: 'token', label: 'Bot token', help: '@BotFather sends it after you choose a name.', secret: 'telegram_bot_token', re: /^\d{5,}:[\w-]{20,}$/ }],
    discord: [
      { key: 'token', label: 'Bot token', help: 'In the developer portal: Bot → Reset Token.', secret: 'discord_bot_token', re: /^\S{50,}$/ },
      { key: 'application_id', label: 'Application ID', help: 'In the developer portal: General Information.', re: /^\d{15,22}$/ },
    ],
  };
  const path = (f: Field) => `channels.${app}.${f.key}`;

  let status = $state<AppStatus | undefined>();
  let secrets = $state<SecretMeta[]>([]);
  let identities = $state<ChannelIdentity[]>([]);
  let spaces = $state<Space[]>([]);
  let users = $state<User[]>([]);
  let view = $state<'loading' | 'steps' | 'manage' | 'failed'>('loading');

  let values = $state<Record<string, string>>({});
  let replacing = $state<Record<string, boolean>>({});
  let reveal = $state<Record<string, boolean>>({});
  let checking = $state(false);
  let outcome = $state<(Failure & { ok: boolean }) | null>(null);
  let slackHref = $state('https://api.slack.com/apps?new_app=1');
  let invite = $state('');
  let timer: ReturnType<typeof setTimeout> | undefined;
  let run = 0;

  const saved = (f: Field) => !!f.secret && !replacing[f.key] && secrets.some((s) => s.name === secretName(path(f), f.secret!));
  const connected = $derived(outcome?.ok || status?.state === 'connected');

  async function load(first = false) {
    const get = <T,>(p: string, d: T) => api.get<T>(p).catch(() => d);
    try {
      const [channels, s, ids, sp, us] = await Promise.all([
        api.get<AppStatus[]>('/api/channels'),
        get<SecretMeta[]>('/api/vault', []),
        get<ChannelIdentity[]>('/api/identities', []),
        get<Space[]>('/api/spaces', []),
        get<User[]>('/api/users', []),
        config.load(true),
      ]);
      [status, secrets, identities, spaces, users] = [channels.find((c) => c.name === app), s, ids, sp, us];
      if (first) {
        const configured = fields[app].some(saved) || status?.state === 'connected';
        view = configured && status?.state !== 'auth_failed' ? 'manage' : 'steps';
        values = Object.fromEntries(fields[app].map((f) => [f.key, f.secret ? '' : String(config.value(path(f), '') ?? '')]));
      }
    } catch {
      if (first) view = 'failed';
    }
  }

  $effect(() => {
    const a = app;
    untrack(() => {
      view = 'loading';
      outcome = null;
      replacing = {};
      void load(true);
      if (a === 'slack')
        api.text('/api/channels/slack/manifest').then((m) => (slackHref = `https://api.slack.com/apps?new_app=1&manifest_yaml=${encodeURIComponent(m)}`), () => {});
    });
    return () => clearTimeout(timer);
  });

  const ready = $derived(fields[app].every((f) => saved(f) || f.re.test((values[f.key] ?? '').trim())) && fields[app].some((f) => !saved(f)));

  function changed(e: Event) {
    clearTimeout(timer);
    outcome = null;
    if (!ready) return;
    if ((e as InputEvent).inputType === 'insertFromPaste') void connect();
    else timer = setTimeout(connect, 800);
  }

  async function connect() {
    clearTimeout(timer);
    const mine = ++run;
    checking = true;
    outcome = null;
    try {
      const set: Record<string, unknown> = { [`channels.${app}.enabled`]: true };
      for (const f of fields[app]) {
        const v = (values[f.key] ?? '').trim();
        if (!f.secret) set[path(f)] = v;
        else {
          const n = secretName(path(f), f.secret);
          if (v) await api.put(`/api/vault/global/${n}`, { value: v });
          set[path(f)] = `secret:${n}`;
        }
      }
      const bad = await saveConfig(set);
      if (mine !== run) return;
      if (bad) {
        outcome = { ok: false, message: `Bothy couldn't save the ${app === 'slack' ? 'tokens' : 'token'}. Try again.`, details: bad.details };
        return;
      }
      const r = await api.post<ChannelTestResp>(`/api/channels/${app}/test`);
      if (mine !== run) return;
      await load();
      outcome = { ok: true, message: `${connectedText({ ...status!, identity: r.identity }, true)}.` };
      replacing = {};
      for (const f of fields[app]) if (f.secret) values[f.key] = '';
      if (app === 'discord') invite = (await api.get<DiscordInvite>('/api/channels/discord/invite').catch(() => ({ url: '' }))).url;
    } catch (e) {
      if (mine === run) outcome = { ok: false, ...channelFailure(app, e) };
    } finally {
      if (mine === run) checking = false;
    }
  }

  const later = 'You can do this once step 2 is done.';
</script>

{#snippet step(n: number, title: string, body: Snippet)}
  <li class="grid grid-cols-[1.25rem_minmax(0,1fr)] gap-x-3 [&+li]:mt-10">
    <span class="text-heading text-muted tabular-nums" aria-hidden="true">{n}</span>
    <div>
      <h2 class="section-title"><span class="sr-only">Step {n}:{' '}</span>{title}</h2>
      <div class="mt-2 space-y-3">{@render body()}</div>
    </div>
  </li>
{/snippet}

{#snippet text(t: string)}<p class="text-body text-muted">{t}</p>{/snippet}

{#snippet one()}
  {#if app === 'slack'}
    {@render text('Slack opens with everything filled in. Choose your workspace, click Create, then Install to Workspace.')}
    <Button href={slackHref} external>Create the app in Slack</Button>
  {:else if app === 'telegram'}
    {@render text('In Telegram, send /newbot to @BotFather and choose a name. It replies with a token.')}
    <Button href="https://t.me/BotFather" external>Open @BotFather</Button>
  {:else}
    {@render text('Create an application, add a bot to it, then turn on Message Content Intent under Bot.')}
    <Button href="https://discord.com/developers/applications" external>Open the developer portal</Button>
  {/if}
{/snippet}

{#snippet two()}
  {#each fields[app] as f (f.key)}
    {#if saved(f)}
      <div>
        <p class="field-label">{f.label}</p>
        <p class="mt-2 text-body text-text">
          <span aria-hidden="true">•••••</span> saved · <LinkButton onclick={() => (replacing[f.key] = true)} aria-label="Replace the {f.label.toLowerCase()}">Replace</LinkButton>
        </p>
      </div>
    {:else}
      <TextField
        label={f.label}
        type={f.secret && !reveal[f.key] ? 'password' : 'text'}
        bind:value={values[f.key]}
        help={f.help}
        autocomplete="off"
        spellcheck="false"
        inputmode={f.secret ? undefined : 'numeric'}
        oninput={changed}
        onkeydown={(e: KeyboardEvent) => e.key === 'Enter' && ready && connect()}
      >
        {#snippet labelAction()}{#if f.secret}<LinkButton onclick={() => (reveal[f.key] = !reveal[f.key])} aria-pressed={!!reveal[f.key]}>{reveal[f.key] ? 'Hide' : 'Show'}</LinkButton>{/if}{/snippet}
      </TextField>
    {/if}
  {/each}
  {#if checking}
    <StatusLine tone="progress" text={app === 'slack' ? 'Checking the tokens…' : 'Checking the token…'} />
  {:else if outcome}
    <StatusLine tone={outcome.ok ? 'success' : 'danger'} text={outcome.message} details={outcome.details} alert />
  {:else if status?.state === 'auth_failed' && !fields[app].some((f) => replacing[f.key])}
    <StatusLine tone="danger" text={channelFailure(app, new Error('rejected')).message} action={{ label: 'Check again', onclick: connect }} />
  {/if}
{/snippet}

{#snippet link()}
  {#if connected}<Pairing channel={app} bot={status?.identity} auto />{:else}{@render text(later)}{/if}
{/snippet}

{#snippet invited()}
  {#if connected && invite}
    {@render text('Discord asks which server to add it to.')}
    <Button href={invite} external>Invite Bothy to your server</Button>
  {:else}{@render text(later)}{/if}
{/snippet}

{#if view === 'manage' && status}
  <ChatAppManage {app} {status} {identities} {spaces} {users} {secrets} onchanged={() => load(true)} />
{:else}
  <ConnPage title="Connect {name}">
    {#if view === 'failed'}
      <EmptyState text="This page didn't load. Check your connection and try again." action={{ label: 'Try again', onclick: () => load(true), variant: 'secondary' }} />
    {:else if view === 'steps'}
      <ol>
        {#if app === 'slack'}
          {@render step(1, 'Create the Slack app', one)}
          {@render step(2, 'Paste the two tokens', two)}
          {@render step(3, 'Link your Slack account', link)}
        {:else if app === 'telegram'}
          {@render step(1, 'Create a bot with @BotFather', one)}
          {@render step(2, 'Paste the bot token', two)}
          {@render step(3, 'Link your Telegram', link)}
        {:else}
          {@render step(1, 'Create a bot in the Discord developer portal', one)}
          {@render step(2, 'Paste the bot token and application ID', two)}
          {@render step(3, 'Invite Bothy to your server', invited)}
          {@render step(4, 'Link your Discord', link)}
        {/if}
      </ol>
    {/if}
  </ConnPage>
{/if}
