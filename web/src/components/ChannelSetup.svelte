<script lang="ts">
  import { api, errorText } from '../lib/api/client';
  import type { ChannelTestResp, DiscordInvite } from '../lib/api/types.gen';
  import { config } from '../lib/config.svelte';
  import Icon from './Icon.svelte';
  import Pairing from './Pairing.svelte';

  let { name }: { name: 'telegram' | 'discord' | 'slack' } = $props();

  const id = $props.id();
  let a = $state('');
  let b = $state('');
  let appId = $state('');
  let identity = $state('');
  let invite = $state('');
  let busy = $state(false);
  let error = $state('');

  // vault names match the defaults in SPEC §7.1
  const secrets = { telegram: ['telegram_bot_token'], discord: ['discord_bot_token'], slack: ['slack_app_token', 'slack_bot_token'] };
  const fields = { telegram: ['token'], discord: ['token'], slack: ['app_token', 'bot_token'] };
  const ready = $derived(name === 'slack' ? /^xapp-/.test(a) && /^xoxb-/.test(b) : !!a && (name !== 'discord' || !!appId));

  async function save() {
    busy = true;
    error = '';
    try {
      const values = [a, b];
      const set: Record<string, unknown> = { [`channels.${name}.enabled`]: true };
      for (let i = 0; i < secrets[name].length; i++) {
        await api.put(`/api/vault/global/${secrets[name][i]}`, { value: values[i].trim() });
        set[`channels.${name}.${fields[name][i]}`] = `secret:${secrets[name][i]}`;
      }
      if (name === 'discord') set['channels.discord.application_id'] = appId.trim();
      if (await config.set(set, true)) return;
      identity = (await api.post<ChannelTestResp>(`/api/channels/${name}/test`)).identity;
      if (name === 'discord') invite = (await api.get<DiscordInvite>('/api/channels/discord/invite')).url;
      a = b = '';
    } catch (e) {
      error = errorText(e);
    } finally {
      busy = false;
    }
  }

  async function slackApp() {
    try {
      const manifest = await api.text('/api/channels/slack/manifest');
      window.open(`https://api.slack.com/apps?new_app=1&manifest_yaml=${encodeURIComponent(manifest)}`, '_blank', 'noopener,noreferrer');
    } catch (e) {
      error = errorText(e);
    }
  }
</script>

<div class="space-y-4">
  {#if !identity}
    {#if name === 'telegram'}
      <ol class="list-decimal space-y-1 pl-5 text-sm text-muted">
        <li>Open <a class="link" href="https://t.me/BotFather" target="_blank" rel="noopener noreferrer">@BotFather</a> in Telegram.</li>
        <li>Send <code>/newbot</code> and pick a name and a username ending in “bot”.</li>
        <li>Copy the token it sends back and paste it here.</li>
      </ol>
    {:else if name === 'discord'}
      <ol class="list-decimal space-y-1 pl-5 text-sm text-muted">
        <li>Create an application in the <a class="link" href="https://discord.com/developers/applications" target="_blank" rel="noopener noreferrer">Discord developer portal</a> and add a bot.</li>
        <li>Turn on the <strong>Message Content</strong> intent; Bothy needs no other privileged intent.</li>
        <li>Copy the bot token and the application id.</li>
      </ol>
    {:else}
      <ol class="list-decimal space-y-1 pl-5 text-sm text-muted">
        <li><button type="button" class="link" onclick={slackApp}>Create the Slack app from Bothy's manifest</button> (Socket Mode, minimal scopes, link unfurls off).</li>
        <li>Create an app-level token with <code>connections:write</code> (starts with xapp-).</li>
        <li>Install the app to your workspace and copy the bot token (starts with xoxb-).</li>
      </ol>
    {/if}
    <div class="grid gap-3 sm:grid-cols-2">
      <div>
        <label class="label" for="{id}-a">{name === 'slack' ? 'App token (xapp-…)' : 'Bot token'}</label>
        <input id="{id}-a" class="input mono" type="password" autocomplete="off" spellcheck="false" bind:value={a} />
      </div>
      {#if name === 'slack'}
        <div>
          <label class="label" for="{id}-b">Bot token (xoxb-…)</label>
          <input id="{id}-b" class="input mono" type="password" autocomplete="off" spellcheck="false" bind:value={b} />
        </div>
      {:else if name === 'discord'}
        <div>
          <label class="label" for="{id}-b">Application id</label>
          <input id="{id}-b" class="input mono" inputmode="numeric" bind:value={appId} />
        </div>
      {/if}
    </div>
    <button type="button" class="btn btn-primary" disabled={busy || !ready} onclick={save}>{busy ? 'Connecting…' : 'Save and test'}</button>
    {#if error}<p class="alert-bad" role="alert">{error}</p>{/if}
  {:else}
    <p class="alert-ok"><Icon name="check" size={16} />Connected as {identity}.</p>
    {#if invite}
      <a class="btn" href={invite} target="_blank" rel="noopener noreferrer">Add the bot to your server<Icon name="external" size={14} /></a>
    {/if}
    <Pairing channel={name} />
  {/if}
</div>
