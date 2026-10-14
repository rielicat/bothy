<script lang="ts">
  import { untrack } from 'svelte';
  import { Button, Dialog, Disclosure, EmptyState, IconButton, Menu, SettingsSection, StatusLine, Switch, toast } from '../../../components/ui';
  import { api, errorText } from '../../../lib/api/client';
  import type { User } from '../../../lib/api/types.gen';
  import { config } from '../../../lib/config.svelte';
  import { fullTime, plural } from '../../../lib/format';
  import { router } from '../../../lib/router.svelte';
  import ConnPage from './ConnPage.svelte';
  import { approverLine, readAt, saveConfig, type Failure } from './connections';
  import EnvironmentDialog from './EnvironmentDialog.svelte';
  import RepoPicker from './RepoPicker.svelte';
  import type { GitHubConnection, GitHubConnectionDetail, GitHubProbe, GitHubRepo, IndexedRepo, ReleaseEnvironment, Releases, Workflow } from './types';

  let {
    conn,
    reading = 0,
    onchanged,
    onreplace,
  }: {
    conn: GitHubConnection;
    /** Repositories just saved, which Bothy starts reading now. */
    reading?: number;
    onchanged: () => void;
    onreplace: () => void;
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

  const id = $derived(encodeURIComponent(conn.id));
  const full = (r: { owner: string; name: string }) => `${r.owner}/${r.name}`;
  const allowed = $derived(repos.filter((r) => r.allowed).map(full));
  const web = $derived.by(() => {
    try {
      const u = new URL(config.value<string>('github.api_base', 'https://api.github.com'));
      return u.host === 'api.github.com' ? 'https://github.com' : `${u.protocol}//${u.host}`;
    } catch {
      return 'https://github.com';
    }
  });

  async function load() {
    const get = <T,>(p: string, d: T) => api.get<T>(p).catch(() => d);
    let detail: GitHubConnectionDetail | null;
    [detail, indexed, envs, users] = await Promise.all([
      get<GitHubConnectionDetail | null>(`/api/github/connections/${id}`, null),
      get<{ items: IndexedRepo[] }>('/api/knowledge/repos', { items: [] }).then((r) => r.items ?? []),
      get<Releases>('/api/releases', { environments: [] }).then((r) => r.environments ?? []),
      get<User[]>('/api/users', []),
    ]);
    repos = [...(detail?.repos ?? [])].sort((a, b) => Number(b.allowed) - Number(a.allowed) || full(a).localeCompare(full(b)));
    const lists = await Promise.all(
      [...new Set(envs.map((e) => e.repo))].map((repo) =>
        get<Workflow[]>(`/api/github/repos/${repo.split('/').map(encodeURIComponent).join('/')}/workflows`, []).then((l) => l.map((w) => [repo, w] as const)),
      ),
    );
    flows = Object.fromEntries(lists.flat().map(([repo, w]) => [`${repo}/${w.path.split('/').pop()}`, w.name]));
  }

  $effect(() => {
    void conn.id;
    untrack(load);
  });

  // The workflow's name from GitHub ("Deploy workflow"); its file name until S19 lists workflows.
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
      toast('Reading it again.');
      await load();
    } catch (e) {
      problem = { message: "Bothy couldn't start reading it again. Try again.", details: errorText(e) };
    }
  }

  async function openAdd() {
    adding = true;
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

  async function addMore() {
    if (!more.length) return;
    busy = true;
    if (await putRepos([...allowed, ...more])) {
      adding = false;
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

  const addable = $derived((probe?.repos ?? []).filter((r) => !r.archived && !allowed.includes(full(r)) && (!r.connection_id || r.connection_id === conn.id)));
</script>

<ConnPage title="GitHub">
  {#if reading}
    <StatusLine
      class="mb-6"
      tone="progress"
      text="Bothy is reading {plural(reading, 'repository', 'repositories')}. You can keep working; this takes a few minutes."
      action={{ label: 'See progress in Knowledge ›', onclick: () => router.navigate('/knowledge') }}
    />
  {/if}
  {#if problem}<StatusLine class="mb-6" tone="danger" alert text={problem.message} details={problem.details} />{/if}

  <SettingsSection title="Account">
    <div class="flex min-h-14 flex-wrap items-center justify-between gap-x-6 gap-y-2 border-b border-border py-3">
      <p class="min-w-0 text-body {conn.status === 'error' ? 'text-danger-text' : 'text-text'}">
        {conn.status === 'error'
          ? `GitHub stopped accepting the ${conn.auth === 'app' ? 'app' : 'token'}. Replace it to reconnect.`
          : `Connected as ${conn.account} (${conn.auth === 'app' ? 'GitHub App' : 'personal access token'})`}
      </p>
      <Button size="sm" onclick={onreplace}>{conn.auth === 'app' ? 'Change connection…' : 'Replace token'}</Button>
    </div>
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
    </dl>
  </Disclosure>
</ConnPage>

<Dialog bind:open={leaving} title="Disconnect GitHub?" initialFocus="cancel" {busy}>
  <p class="text-body text-text">Bothy will stop reading these repositories and can't open pull requests or deploy. Project notes are kept.</p>
  {#snippet footer({ close })}
    <Button data-cancel onclick={close}>Cancel</Button>
    <Button variant="danger" loading={busy} loadingLabel="Disconnecting…" onclick={disconnect}>Disconnect GitHub</Button>
  {/snippet}
</Dialog>

<Dialog bind:open={adding} title="Add repositories" size="md" {busy}>
  {#if probeError}
    <StatusLine tone="danger" text="Bothy couldn't ask GitHub for more repositories. Try again." details={probeError} />
  {:else if !probe}
    <p class="text-body text-muted">Asking GitHub…</p>
  {:else if !addable.length}
    <p class="text-body text-text">Bothy already uses every repository this connection can see. Give it access to more on GitHub first.</p>
  {:else}
    <RepoPicker repos={addable} bind:selected={more} />
  {/if}
  {#snippet footer({ close })}
    <Button data-cancel onclick={close}>Cancel</Button>
    {#if addable.length}<Button variant="primary" loading={busy} loadingLabel="Adding…" onclick={addMore}>Add repositories</Button>{/if}
  {/snippet}
</Dialog>

<Dialog open={!!dropping} onclose={() => (dropping = '')} title="Delete {dropping}?" initialFocus="cancel" {busy}>
  <p class="text-body text-text">Bothy can no longer deploy to {dropping}. Past deploys stay in Activity.</p>
  {#snippet footer({ close })}
    <Button data-cancel onclick={close}>Cancel</Button>
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
