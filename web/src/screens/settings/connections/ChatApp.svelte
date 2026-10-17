<script lang="ts">
  import { tick, untrack, type Snippet } from 'svelte';
  import { Button, EmptyState, LinkButton, StatusLine, TextField, toast } from '../../../components/ui';
  import { api } from '../../../lib/api/client';
  import type { ChannelTestResp, DiscordInvite, SecretMeta, SpaceView } from '../../../lib/api/types.gen';
  import { config } from '../../../lib/config.svelte';
  import { router } from '../../../lib/router.svelte';
  import { pictureDownloadURL, pictureURL } from '../picture';
  import ChatAppManage from './ChatAppManage.svelte';
  import ConnPage from './ConnPage.svelte';
  import { appTitle, channelFailure, connectedText, isApp, saveConfig, secretName, type AppName, type AppStatus, type Failure } from './connections';

  let { params }: { params: Record<string, string> } = $props();
  const app = $derived((isApp(params.item) ? params.item : 'slack') as AppName);
  const name = $derived(appTitle[app]);

  type Field = { key: string; label: string; help: string; secret?: string; re: RegExp };
  const fields: Record<AppName, Field[]> = {
    slack: [
      { key: 'bot_token', label: 'Bot token', help: 'Starts with xoxb-. In Slack: OAuth & Permissions → Bot User OAuth Token.', secret: 'slack_bot_token', re: /^xoxb-\S{16,}$/ },
      {
        key: 'app_token',
        label: 'App-level token',
        help: 'Starts with xapp-. In Slack: Basic Information → App-Level Tokens → Generate Token and Scopes. Add the connections:write scope.',
        secret: 'slack_app_token',
        re: /^xapp-\S{16,}$/,
      },
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
  let spaces = $state<SpaceView[]>([]);
  let view = $state<'loading' | 'steps' | 'manage' | 'failed'>('loading');

  let values = $state<Record<string, string>>({});
  let replacing = $state<Record<string, boolean>>({});
  let reveal = $state<Record<string, boolean>>({});
  let checking = $state(false);
  let outcome = $state<(Failure & { ok: boolean }) | null>(null);
  let slackHref = $state('https://api.slack.com/apps?new_app=1');
  let invite = $state('');
  // The bot the check just reached, before the status catches up.
  let bot = $state('');
  let fromManage = $state(false);
  let timer: ReturnType<typeof setTimeout> | undefined;
  let run = 0;

  const saved = (f: Field) => !!f.secret && !replacing[f.key] && secrets.some((s) => s.name === secretName(path(f), f.secret!));
  const connected = $derived(outcome?.ok || (status?.state === 'connected' && !fromManage));

  async function load(first = false) {
    const get = <T,>(p: string, d: T) => api.get<T>(p).catch(() => d);
    try {
      const [channels, s, sp] = await Promise.all([
        api.get<AppStatus[]>('/api/channels'),
        get<SecretMeta[]>('/api/vault', []),
        get<SpaceView[]>('/api/spaces', []),
        config.load(true),
      ]);
      [status, secrets, spaces] = [channels.find((c) => c.name === app), s, sp];
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
      fromManage = false;
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
    // A first connection whose test fails is undone, so a token the app
    // refused is neither kept nor left switched on.
    const firstTime = status?.state !== 'connected' && !fromManage;
    const added: string[] = [];
    const undo = async () => {
      if (!firstTime) return;
      await saveConfig({ [`channels.${app}.enabled`]: false }).catch(() => {});
      for (const n of added) await api.del(`/api/vault/global/${n}`).catch(() => {});
    };
    try {
      const set: Record<string, unknown> = { [`channels.${app}.enabled`]: true };
      for (const f of fields[app]) {
        const v = (values[f.key] ?? '').trim();
        if (!f.secret) set[path(f)] = v;
        else {
          const n = secretName(path(f), f.secret);
          if (v) {
            if (!secrets.some((x) => x.name === n)) added.push(n);
            await api.put(`/api/vault/global/${n}`, { value: v });
          }
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
      replacing = {};
      for (const f of fields[app]) if (f.secret) values[f.key] = '';
      if (fromManage) {
        fromManage = false;
        outcome = null;
        view = 'manage';
        toast(`${name} reconnected.`);
        return;
      }
      bot = r.identity;
      outcome = { ok: true, message: `${connectedText({ ...status!, identity: r.identity }, true)}.` };
      if (app === 'discord') invite = (await api.get<DiscordInvite>('/api/channels/discord/invite').catch(() => ({ url: '' }))).url;
    } catch (e) {
      await undo();
      if (mine === run) {
        outcome = { ok: false, ...channelFailure(app, e) };
        await load();
      }
    } finally {
      if (mine === run) checking = false;
    }
  }

  async function replace(f: Field) {
    replacing[f.key] = true;
    await tick();
    document.getElementById(`${app}-${f.key}`)?.focus();
  }

  const later = 'You can do this once step 2 is done.';
  // t.me opens the bot's chat from its username.
  const handle = $derived(bot || status?.identity || '');
  const telegramHref = $derived(handle.startsWith('@') ? `https://t.me/${encodeURIComponent(handle.slice(1))}` : '');
</script>

{#snippet step(n: number, title: string, body: Snippet, optional?: boolean)}
  <li class="grid grid-cols-[1.25rem_minmax(0,1fr)] gap-x-3 [&+li]:mt-10">
    <span class="text-heading text-muted tabular-nums" aria-hidden="true">{n}</span>
    <div>
      <h2 class="section-title"><span class="sr-only">Step {n}:{' '}</span>{title}{#if optional}<span class="font-normal text-muted">{' (optional)'}</span>{/if}</h2>
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
    {@render text('Under Bot, turn off Public Bot, so only you can add Bothy to servers.')}
    <Button href="https://discord.com/developers/applications" external>Open the developer portal</Button>
  {/if}
{/snippet}

<!-- Slack lets only a person set the app's icon; it opens on Basic Information after Create. -->
{#snippet picture()}
  {@render text('In Slack, open Basic Information and upload it under App icon.')}
  <div class="flex flex-wrap items-center gap-3">
    <img src={pictureURL()} alt={config.value<string>('agent.name', 'Bothy')} width="48" height="48" class="size-12 shrink-0 rounded-lg border border-border bg-subtle" />
    <Button href={pictureDownloadURL} download>Download picture</Button>
  </div>
{/snippet}

{#snippet two()}
  {#each fields[app] as f (f.key)}
    {#if saved(f)}
      <div>
        <p class="field-label">{f.label}</p>
        <p class="mt-2 text-body text-text">
          <span aria-hidden="true">•••••</span> saved · <LinkButton onclick={() => replace(f)} aria-label="Replace the {f.label.toLowerCase()}">Replace</LinkButton>
        </p>
      </div>
    {:else}
      <TextField
        id="{app}-{f.key}"
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

{#snippet invited()}
  {#if connected && invite}
    {@render text('Discord asks which server to add it to.')}
    <Button href={invite} external>Invite Bothy to your server</Button>
  {:else}{@render text(later)}{/if}
{/snippet}

{#if view === 'manage' && status}
  <ChatAppManage
    {app}
    {status}
    {spaces}
    {secrets}
    onchanged={() => load(true)}
    onreplace={(f) => {
      outcome = { ok: false, message: f.message, details: f.details };
      fromManage = true;
      view = 'steps';
    }}
  />
{:else}
  <ConnPage title="Connect {name}">
    {#if view === 'failed'}
      <EmptyState text="This page didn't load. Check your connection and try again." action={{ label: 'Try again', onclick: () => load(true), variant: 'secondary' }} />
    {:else if view === 'steps'}
      <ol>
        {#if app === 'slack'}
          {@render step(1, 'Create the Slack app', one)}
          <!-- Replacing tokens: the app, and whatever icon it has, exist already. -->
          {#if !fromManage}{@render step(2, "Add Bothy's picture", picture, true)}{/if}
          {@render step(fromManage ? 2 : 3, 'Paste the two tokens', two)}
        {:else if app === 'telegram'}
          {@render step(1, 'Create a bot with @BotFather', one)}
          {@render step(2, 'Paste the bot token', two)}
        {:else}
          {@render step(1, 'Create a bot in the Discord developer portal', one)}
          {@render step(2, 'Paste the bot token and application ID', two)}
          {@render step(3, 'Invite Bothy to your server', invited)}
        {/if}
      </ol>
      {#if connected}
        <div class="mt-10 space-y-3 pl-8">
          <!-- Telegram can't say who is in the team, nor Discord before a channel is on, so the admin waits to be let in like anyone. -->
          <p class="text-body text-text">
            {#if app === 'slack'}Message Bothy in {name} to try it.{:else}Message Bothy in {name}, then let yourself in from <a class="link" href="/approvals" onclick={router.link}
                >Approvals</a
              >.{/if}
            To approve requests there, turn on Can approve for yourself in <a class="link" href="/settings/people" onclick={router.link}>People</a>.
          </p>
          {#if app === 'telegram' && telegramHref}<Button href={telegramHref} external>Open Telegram</Button>{/if}
        </div>
      {/if}
    {/if}
  </ConnPage>
{/if}
