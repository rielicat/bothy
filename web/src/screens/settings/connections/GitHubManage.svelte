<script lang="ts">
  import { untrack, type Snippet } from 'svelte';
  import { Button, Dialog, Disclosure, EmptyState, IconButton, Menu, SettingsSection, StatusLine, Switch, toast } from '../../../components/ui';
  import { api, errorText } from '../../../lib/api/client';
  import type { User } from '../../../lib/api/types.gen';
  import { config } from '../../../lib/config.svelte';
  import { fullTime, plural } from '../../../lib/format';
  import { router } from '../../../lib/router.svelte';
  import ConnPage from './ConnPage.svelte';
  import { approverLine, githubWeb, installationSettingsURL, readAt, saveConfig, type Failure } from './connections';
  import EnvironmentDialog from './EnvironmentDialog.svelte';
  import FailureLine from './FailureLine.svelte';
  import RepoPicker from './RepoPicker.svelte';
  import type { GitHubApp, GitHubAppCheck, GitHubConnection, GitHubConnectionDetail, GitHubProbe, GitHubRepo, IndexedRepo, ReleaseEnvironment, Releases, Workflow } from './types';

  let {
    conn,
    app,
    landing,
    reading = [],
    onchanged,
    onreplace,
    top,
  }: {
    conn: GitHubConnection;
    /** The app Bothy created for this connection, if it did. */
    app?: GitHubApp;
    /** The app's installation just made this connection, or changed its repositories on GitHub: the picker opens. */
    landing?: 'installed' | 'updated';
    /** Repositories just saved; Bothy reads those it hasn't read yet. */
    reading?: string[];
    onchanged: () => void;
    onreplace: () => void;
    /** Lines above the sections: a GitHub round trip's outcome, apps to delete on GitHub. */
    top?: Snippet;
  } = $props();

  let repos = $state<GitHubRepo[]>([]);
  let indexed = $state<IndexedRepo[]>([]);
  let envs = $state<ReleaseEnvironment[]>([]);
  let users = $state<User[]>([]);
  let problem = $state<Failure | null>(null);
  let leaving = $state(false);
  let busy = $state(false);
  let adding = $state(false);
  let probe = $state<GitHubProbe | null>(null);
  let probeError = $state('');
  let more = $state<string[]>([]);
  let editing = $state<ReleaseEnvironment | null>(null);
  let envOpen = $state(false);
  let dropping = $state('');
  let details = $state(false);
  let flows = $state<Record<string, string>>({});
  let added = $state<string[]>([]);
  let checking = $state(false);
  let notInstalled = $state(false);

  const id = $derived(encodeURIComponent(conn.id));
  const full = (r: { owner: string; name: string }) => `${r.owner}/${r.name}`;
  const allowed = $derived(repos.filter((r) => r.allowed).map(full));
  // Saving reads only repositories Bothy hasn't read, so ones already read don't count.
  const unread = $derived((added.length ? added : reading).filter((n) => !indexed.some((x) => x.repo === n.toLowerCase() && (x.status === 'ready' || x.status === 'error'))).length);
  const web = $derived(githubWeb());
  const installation = $derived(conn.auth === 'app' && conn.installation_id ? installationSettingsURL(conn) : '');

  async function load() {
    const get = <T,>(p: string, d: T) => api.get<T>(p).catch(() => d);
    let detail: GitHubConnectionDetail | null;
    [detail, indexed, envs, users] = await Promise.all([
      get<GitHubConnectionDetail | null>(`/api/github/connections/${id}`, null),
      get<{ items: IndexedRepo[] }>('/api/knowledge/repos', { items: [] }).then((r) => r.items ?? []),
      get<Releases>('/api/releases', { environments: [], deploys: [] }).then((r) => r.environments ?? []),
      get<User[]>('/api/users', []),
    ]);
    repos = (detail?.repos ?? []).filter((r): r is GitHubRepo => !!r).sort((a, b) => Number(b.allowed) - Number(a.allowed) || full(a).localeCompare(full(b)));
    const lists = await Promise.all(
      [...new Set(envs.map((e) => e.repo))].map((repo) =>
        get<Workflow[]>(`/api/github/repos/${repo.split('/').map(encodeURIComponent).join('/')}/workflows`, []).then((l) => l.map((w) => [repo, w] as const)),
      ),
    );
    flows = Object.fromEntries(lists.flat().map(([repo, w]) => [`${repo}/${w.path.split('/').pop()}`, w.name]));
  }

  $effect(() => {
    void conn.id;
    untrack(async () => {
      await load();
      if (landing) void landed();
    });
  });

  // GitHub may have stopped accepting the app because it was uninstalled:
  // a new installation moves the connection, then the connection is tested.
  async function checkAgain() {
    if (!app) return;
    checking = true;
    problem = null;
    notInstalled = false;
    try {
      const r = await api.post<GitHubAppCheck>(`/api/github/apps/${encodeURIComponent(app.id)}/check`);
      if (!r.installed) {
        notInstalled = true;
        return;
      }
      await api.post(`/api/github/connections/${id}/test`);
      onchanged();
    } catch (e) {
      problem = { message: "Bothy couldn't check with GitHub. Try again.", details: errorText(e) };
    } finally {
      checking = false;
    }
  }

  // The workflow's name from GitHub ("Deploy workflow"); its file name when GitHub can't list them.
  function workflowLine(e: ReleaseEnvironment): string {
    const n = flows[`${e.repo}/${e.workflow}`];
    if (!n) return e.workflow;
    return /workflow/i.test(n) ? n : `${n} workflow`;
  }

  function readLine(r: GitHubRepo): { text: string; danger?: boolean } {
    if (!r.allowed) return { text: 'Not in use' };
    const k = indexed.find((x) => x.repo === full(r).toLowerCase());
    if (!k) return { text: 'Not read yet' };
    if (k.status === 'error') return { text: "Couldn't read it", danger: true };
    if (k.status !== 'ready') return { text: 'Reading…' };
    return { text: readAt(k.indexed_at) };
  }

  async function putRepos(list: string[]): Promise<boolean> {
    problem = null;
    try {
      await api.put(`/api/github/connections/${id}/repos`, { repos: list });
      await load();
      return true;
    } catch (e) {
      problem = { message: "Couldn't save. Try again.", details: errorText(e) };
      return false;
    }
  }

  async function setAllowed(r: GitHubRepo, on: boolean) {
    if (!(await putRepos(on ? [...allowed, full(r)] : allowed.filter((x) => x !== full(r))))) r.allowed = !on;
  }

  async function readAgain(r: GitHubRepo) {
    const k = indexed.find((x) => x.repo === full(r).toLowerCase());
    if (!k) return;
    try {
      await api.post(`/api/knowledge/repos/${k.id}/reindex`);
      toast('Started reading again.');
      await load();
    } catch (e) {
      problem = { message: "Bothy couldn't start reading it again. Try again.", details: errorText(e) };
    }
  }

  async function ask() {
    probe = null;
    probeError = '';
    more = [];
    try {
      probe = await api.post<GitHubProbe>(`/api/github/connections/${id}/test`);
      if (!probe.ok) probeError = probe.error ?? '';
    } catch (e) {
      probeError = errorText(e);
    }
  }

  async function openAdd() {
    adding = true;
    await ask();
  }

  // Back from GitHub, the picker opens only when it has something to offer:
  // a switch that kept every repository just says so.
  async function landed() {
    await ask();
    if (probe?.ok && !addable.length && allowed.length) toast(`Connected. Bothy keeps using ${plural(allowed.length, 'repository', 'repositories')}.`);
    else adding = true;
  }

  async function addMore() {
    if (!more.length) return;
    busy = true;
    if (await putRepos([...allowed, ...more])) {
      adding = false;
      added = [...more];
      toast(`${plural(more.length, 'repository', 'repositories')} added.`);
    }
    busy = false;
  }

  async function disconnect() {
    busy = true;
    try {
      await api.del(`/api/github/connections/${id}`);
      leaving = false;
      toast('GitHub disconnected.');
      onchanged();
    } catch (e) {
      problem = { message: "Bothy couldn't disconnect GitHub. Try again.", details: errorText(e) };
      leaving = false;
    } finally {
      busy = false;
    }
  }

  async function dropEnv() {
    busy = true;
    problem = await saveConfig({ [`releases.environments.${dropping}`]: null });
    busy = false;
    if (!problem) toast('Environment deleted.');
    dropping = '';
    await load();
  }

  $effect(() => {
    if (details && !probe) api.post<GitHubProbe>(`/api/github/connections/${id}/test`).then((p) => (probe = p), () => {});
  });

  // A repository already on the page, on or off, has its switch; listing it here too would give it two controls.
  const listed = $derived(new Set(repos.map(full)));
  const addable = $derived((probe?.repos ?? []).filter((r) => !r.archived && !listed.has(full(r)) && (!r.connection_id || r.connection_id === conn.id)));
  const anyOff = $derived(repos.some((r) => !r.allowed));
</script>

<ConnPage title="GitHub">
  {@render top?.()}
  {#if unread}
    <StatusLine
      class="mb-6"
      tone="progress"
      text="Bothy is reading {plural(unread, 'repository', 'repositories')}. You can keep working; this takes a few minutes."
      action={{ label: 'See progress in Knowledge ›', onclick: () => router.navigate('/knowledge') }}
    />
  {/if}
  <FailureLine class="mb-6" failure={problem} />

  <SettingsSection title="Account">
    <div class="flex min-h-14 flex-wrap items-center justify-between gap-x-6 gap-y-2 border-b border-border py-3">
      <div class="min-w-0">
        {#if conn.status === 'error'}
          <p class="text-body text-danger-text">
            {app
              ? `GitHub stopped accepting ${app.name}. If it was uninstalled, install it again.`
              : `GitHub stopped accepting the ${conn.auth === 'app' ? 'app' : 'token'}. Replace it to reconnect.`}
          </p>
        {:else if app}
          <p class="text-body text-text">Connected to {conn.account} as {app.name}</p>
          <p class="text-small text-muted">Pull requests and comments show as {app.slug}[bot].</p>
        {:else}
          <p class="text-body text-text">{conn.auth === 'app' ? `Connected to ${conn.account} (GitHub App)` : `Connected as ${conn.account} (personal access token)`}</p>
        {/if}
      </div>
      <div class="flex flex-wrap items-center gap-2">
        {#if app && conn.status === 'error'}
          <Button size="sm" href={app.install_url} external>Install on GitHub</Button>
          <Button size="sm" loading={checking} loadingLabel="Checking…" onclick={checkAgain}>Check again</Button>
        {:else}
          <Button size="sm" onclick={onreplace}>{conn.auth === 'app' ? 'Change connection…' : 'Replace token'}</Button>
        {/if}
      </div>
    </div>
    {#if app && notInstalled && conn.status === 'error'}<StatusLine class="mt-3" tone="warning" text="{app.name} isn't installed yet." />{/if}
    <div class="pt-3">
      <Button variant="danger-text" class="-ml-4" onclick={() => (leaving = true)}>Disconnect GitHub…</Button>
    </div>
  </SettingsSection>

  <SettingsSection title="Repositories">
    {#each repos as r (full(r))}
      {@const read = readLine(r)}
      <div class="flex min-h-14 items-center gap-3 border-b border-border py-2 pointer-coarse:min-h-16">
        <div class="min-w-0 flex-1">
          <p class="truncate text-body font-medium text-text" title={full(r)}>{full(r)}</p>
          <p class="text-small {read.danger ? 'text-danger-text' : 'text-muted'}">{read.text}</p>
        </div>
        <Switch label="Bothy can use {full(r)}" hideLabel checked={r.allowed} onchange={(v) => setAllowed(r, v)} />
        <Menu
          placement="bottom-end"
          items={[
            ...(r.allowed && indexed.some((x) => x.repo === full(r).toLowerCase()) ? [{ label: 'Read again', onselect: () => readAgain(r) }] : []),
            { label: 'View on GitHub ↗', onselect: () => window.open(`${web}/${full(r)}`, '_blank', 'noopener,noreferrer') },
          ]}
        >
          {#snippet trigger(props)}<IconButton icon="more" label="Options for {full(r)}" size="sm" variant="quiet" {...props} />{/snippet}
        </Menu>
      </div>
    {:else}
      <p class="py-3 text-body text-muted">Bothy can't use any repositories yet.</p>
    {/each}
    <Button class="mt-4" onclick={openAdd}>Add repositories</Button>
  </SettingsSection>

  <section class="mt-10" aria-labelledby="gh-envs">
    <div class="flex min-h-9 items-center justify-between gap-4">
      <h2 id="gh-envs" class="section-title">Deploy environments</h2>
      {#if envs.length}<Button size="sm" onclick={() => ((editing = null), (envOpen = true))}>Add environment</Button>{/if}
    </div>
    <div class="mt-2">
      {#each envs as e (e.name)}
        <div class="flex min-h-14 items-center gap-3 border-b border-border py-2 last:border-b-0">
          <div class="min-w-0 flex-1">
            <p class="text-body font-medium text-text">{e.name}</p>
            <p class="text-small text-muted [overflow-wrap:anywhere]">{e.repo} · {workflowLine(e)} · {approverLine(e)}</p>
          </div>
          <Menu
            placement="bottom-end"
            items={[
              { label: 'Edit…', onselect: () => ((editing = e), (envOpen = true)) },
              { label: 'Delete environment…', danger: true, onselect: () => (dropping = e.name) },
            ]}
          >
            {#snippet trigger(props)}<IconButton icon="more" label="Options for {e.name}" size="sm" variant="quiet" {...props} />{/snippet}
          </Menu>
        </div>
      {:else}
        <EmptyState text="No deploy environments yet. Add one so Bothy can deploy with approval." action={{ label: 'Add environment', onclick: () => ((editing = null), (envOpen = true)) }} />
      {/each}
    </div>
  </section>

  <Disclosure class="mt-10" bind:open={details}>
    <dl class="space-y-1 text-small text-muted" data-copy-ignore>
      <div>
        <dt class="inline">Requests left this hour:</dt>
        <dd class="inline">{probe?.rate_limit?.limit ? `${probe.rate_limit.remaining.toLocaleString()} of ${probe.rate_limit.limit.toLocaleString()}` : '…'}</dd>
      </div>
      <div><dt class="inline">Last checked:</dt> <dd class="inline">{fullTime(conn.checked_at)}</dd></div>
      <div><dt class="inline">GitHub address:</dt> <dd class="inline">{config.value<string>('github.api_base', 'https://api.github.com')}</dd></div>
      {#if app}
        <div><dt class="inline">GitHub App:</dt> <dd class="inline">{app.name}</dd></div>
        <div>
          <a class="link -my-0.5 inline-flex min-h-6 items-center pointer-coarse:-my-3 pointer-coarse:min-h-11" href={app.settings_url} target="_blank" rel="noopener noreferrer"
            >App settings<span aria-hidden="true">&nbsp;↗</span><span class="sr-only"> (opens in a new tab)</span></a
          >
        </div>
      {/if}
    </dl>
  </Disclosure>
</ConnPage>

<Dialog bind:open={leaving} title="Disconnect GitHub?" initialFocus="cancel" {busy} cancel>
  <p class="text-body text-text">
    Bothy will stop reading these repositories and can't open pull requests or deploy. Project notes are kept.{app ? ` ${app.name} stays on GitHub until you delete it there.` : ''}
  </p>
  {#snippet footer()}
    <Button variant="danger" loading={busy} loadingLabel="Disconnecting…" onclick={disconnect}>Disconnect GitHub</Button>
  {/snippet}
</Dialog>

<Dialog bind:open={adding} title={allowed.length ? 'Add repositories' : 'Which repositories can Bothy use?'} size="md" {busy} cancel>
  {#if probeError}
    <StatusLine tone="danger" text="Bothy couldn't ask GitHub for more repositories. Try again." details={probeError} />
  {:else if !probe}
    <p class="text-body text-muted">Asking GitHub…</p>
  {:else if !addable.length && installation}
    <p class="text-body text-text">
      {anyOff
        ? 'Every repository the app can reach is already listed. Turn one on with its switch, or give the app access to more on GitHub.'
        : 'Bothy already uses every repository the app can reach. Give it access to more on GitHub, then come back.'}
    </p>
    <Button class="mt-4" href={installation} external>Choose repositories on GitHub</Button>
  {:else if !addable.length}
    <p class="text-body text-text">
      {anyOff
        ? 'Every repository this connection can see is already listed. Turn one on with its switch, or give it access to more on GitHub.'
        : 'Bothy already uses every repository this connection can see. Give it access to more on GitHub first.'}
    </p>
  {:else}
    <RepoPicker repos={addable} bind:selected={more} />
    {#if installation}
      <p class="mt-3 text-small text-muted">
        Missing a repository? <a class="link -my-0.5 inline-flex min-h-6 items-center pointer-coarse:-my-3 pointer-coarse:min-h-11" href={installation} target="_blank" rel="noopener noreferrer"
          >Give the app access on GitHub<span aria-hidden="true">&nbsp;↗</span><span class="sr-only"> (opens in a new tab)</span></a
        >
      </p>
    {/if}
  {/if}
  {#snippet footer()}
    {#if addable.length}<Button variant="primary" loading={busy} loadingLabel="Adding…" onclick={addMore}>Add repositories</Button>{/if}
  {/snippet}
</Dialog>

<Dialog open={!!dropping} onclose={() => (dropping = '')} title="Delete {dropping}?" initialFocus="cancel" {busy} cancel>
  <p class="text-body text-text">Bothy can no longer deploy to {dropping}. Past deploys stay in Activity. This can't be undone.</p>
  {#snippet footer()}
    <Button variant="danger" loading={busy} loadingLabel="Deleting…" onclick={dropEnv}>Delete environment</Button>
  {/snippet}
</Dialog>

<EnvironmentDialog
  bind:open={envOpen}
  env={editing}
  repos={allowed}
  {users}
  taken={envs.filter((e) => e.name !== editing?.name).map((e) => e.name)}
  onsaved={load}
  ondelete={(n) => (dropping = n)}
/>
