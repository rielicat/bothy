<script lang="ts">
  import { untrack } from 'svelte';
  import { Button, EmptyState, SettingsSection } from '../../../components/ui';
  import { api } from '../../../lib/api/client';
  import type { MCPStatus, SecretMeta, Teammate } from '../../../lib/api/types.gen';
  import { config } from '../../../lib/config.svelte';
  import { events } from '../../../lib/sse.svelte';
  import AddMcpDialog from './AddMcpDialog.svelte';
  import ConnPage from './ConnPage.svelte';
  import Loading from './Loading.svelte';
  import {
    appNames,
    appStatus,
    appTitle,
    githubStatus,
    searchTitle,
    secretName,
    serverTitle,
    toneClass,
    toolStatus,
    type AppStatus,
    type RowStatus,
  } from './connections';
  import type { GitHubApp, GitHubConnection, GitHubRepo } from './types';

  let channels = $state<AppStatus[]>([]);
  let teammates = $state<Teammate[]>([]);
  let secrets = $state<SecretMeta[]>([]);
  let mcp = $state<MCPStatus[]>([]);
  let github = $state<GitHubConnection[]>([]);
  let repos = $state<GitHubRepo[]>([]);
  let githubApps = $state<GitHubApp[]>([]);
  let phase = $state<'loading' | 'ready' | 'failed'>('loading');
  let adding = $state(false);

  async function load() {
    const get = <T,>(path: string, fallback: T) => api.get<T>(path).catch(() => fallback);
    try {
      [channels, teammates, secrets, mcp, github, repos, githubApps] = await Promise.all([
        api.get<AppStatus[]>('/api/channels'),
        get<Teammate[]>('/api/teammates', []),
        get<SecretMeta[]>('/api/vault', []),
        get<MCPStatus[]>('/api/mcp', []),
        get<GitHubConnection[]>('/api/github/connections', []),
        get<GitHubRepo[]>('/api/github/repos', []),
        get<GitHubApp[]>('/api/github/apps', []),
        config.load(true),
      ]);
      phase = 'ready';
    } catch {
      phase = 'failed';
    }
  }

  $effect(() => {
    untrack(load);
    const offs = ['channel.status', 'mcp.pin_pending', 'config.changed'].map((ev) => events.on(ev, () => void load()));
    return () => {
      offs.forEach((off) => off());
    };
  });

  // Saved tokens make an app that is turned off read "Off", not "Not connected".
  const tokenPaths: Record<string, [string, string]> = {
    slack: ['channels.slack.bot_token', 'slack_bot_token'],
    telegram: ['channels.telegram.token', 'telegram_bot_token'],
    discord: ['channels.discord.token', 'discord_bot_token'],
  };
  const saved = (app: string) => secrets.some((s) => s.name === secretName(...tokenPaths[app]));
  const people = (app: string) => teammates.filter((t) => t.channel === app && t.status === 'teammate' && !t.left).length;
  const servers = $derived(mcp.map((s) => s.name).sort());
  const search = $derived(searchTitle[config.value<string>('search.provider', 'none')] ?? 'Off');
</script>

{#snippet row(name: string, st: RowStatus, href: string)}
  <div
    class="grid min-h-14 grid-cols-[minmax(0,1fr)_auto] items-center gap-x-6 gap-y-0.5 border-b border-border py-3 last:border-b-0 sm:grid-cols-[7.5rem_minmax(0,1fr)_auto]"
  >
    <span class="text-body font-medium text-text">{name}</span>
    <span class="text-small max-sm:col-start-1 max-sm:row-start-2 {toneClass[st.tone]}">{st.text}</span>
    <Button size="sm" {href} aria-label="{st.action} {name}" class="max-sm:col-start-2 max-sm:row-span-2 max-sm:row-start-1">{st.action}</Button>
  </div>
{/snippet}

<ConnPage title="Connections" back={null}>
  {#if phase === 'failed'}
    <EmptyState text="This page didn't load. Check your connection and try again." action={{ label: 'Try again', onclick: load, variant: 'secondary' }} />
  {:else if phase === 'loading'}
    <Loading />
  {:else}
    <SettingsSection title="Chat apps">
      {#each appNames as app (app)}
        {@const st = channels.find((c) => c.name === app)}
        {@render row(appTitle[app], appStatus(app, st, people(app), !!st?.enabled || saved(app)), `/settings/connections/${app}`)}
      {/each}
    </SettingsSection>

    <SettingsSection title="Code">
      {@render row('GitHub', githubStatus(github[0], repos.length, githubApps.find((a) => a.state === 'pending')), '/settings/connections/github')}
    </SettingsSection>

    <SettingsSection title="Tools">
      <div>
        {#each servers as name (name)}
          {@const st = toolStatus(mcp.find((s) => s.name === name))}
          {@render row(serverTitle(name), st, `/settings/connections/mcp-${name}${st.action === 'Review' ? '?review=1' : ''}`)}
        {/each}
        {@render row('Web search', { text: search, tone: 'muted', action: 'Change' }, '/settings/connections/search')}
      </div>
      {#if servers.length}
        <Button class="mt-4" onclick={() => (adding = true)}>Add an MCP server</Button>
      {:else}
        <EmptyState
          class="mt-4"
          text="No tools connected."
          more="MCP servers let Bothy use other services, like Linear or your database."
          action={{ label: 'Add an MCP server', onclick: () => (adding = true), variant: 'secondary' }}
        />
      {/if}
    </SettingsSection>
  {/if}
</ConnPage>

<AddMcpDialog bind:open={adding} onadded={load} />
