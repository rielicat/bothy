<script lang="ts">
  import { untrack } from 'svelte';
  import {
    Button,
    Disclosure,
    EmptyState,
    KeyField,
    LinkButton,
    OptionList,
    Select,
    StatusLine,
    TextArea,
    TextField,
    toast,
    type CheckResult,
  } from '../../../components/ui';
  import { api, errorText } from '../../../lib/api/client';
  import { config } from '../../../lib/config.svelte';
  import { router } from '../../../lib/router.svelte';
  import { session } from '../../../lib/stores/session.svelte';
  import ConnPage from './ConnPage.svelte';
  import { appLanding, githubOutcome, saveConfig, type AppLandingFailure, type Failure, type GitHubOutcome } from './connections';
  import GitHubAppPending from './GitHubAppPending.svelte';
  import GitHubAppRetired from './GitHubAppRetired.svelte';
  import GitHubCreateApp from './GitHubCreateApp.svelte';
  import GitHubManage from './GitHubManage.svelte';
  import Loading from './Loading.svelte';
  import RepoPicker from './RepoPicker.svelte';
  import type { GitHubApp, GitHubConnection, GitHubProbe } from './types';

  let conns = $state<GitHubConnection[]>([]);
  let apps = $state<GitHubApp[]>([]);
  let view = $state<'loading' | 'connect' | 'manage' | 'failed'>('loading');
  let replacing = $state<GitHubConnection | null>(null);
  let reading = $state(0);
  // Where a round trip through GitHub came back (?github_app=, §12.2), read once.
  let landing = $state<string | null>(null);
  let landed = $state<AppLandingFailure | null>(null);

  let auth = $state('pat');
  let token = $state('');
  let appId = $state('');
  let key = $state('');
  let paste = $state(false);
  let installation = $state('');
  let probe = $state<GitHubProbe | null>(null);
  let appResult = $state<GitHubOutcome | null>(null);
  let appChecking = $state(false);
  let chosen = $state<string[]>([]);
  let saving = $state(false);
  let failure = $state<Failure | null>(null);
  let address = $state('');
  let addressStatus = $state<Failure | null>(null);
  let timer: ReturnType<typeof setTimeout> | undefined;
  let run = 0;

  async function load() {
    try {
      [conns, apps] = await Promise.all([
        api.get<GitHubConnection[]>('/api/github/connections'),
        api.get<GitHubApp[]>('/api/github/apps').catch(() => [] as GitHubApp[]),
        config.load(true),
      ]);
      view = conns.length && !replacing ? 'manage' : 'connect';
      // Opening the repository picker belongs to the connection the landing made.
      if (view === 'connect' && (landing === 'installed' || landing === 'updated')) landing = null;
    } catch {
      conns = [];
      view = 'connect';
    }
    address = config.value<string>('github.api_base', '');
  }

  $effect(() => {
    untrack(() => {
      const code = router.query.get('github_app');
      if (code) {
        landing = code;
        landed = appLanding(code, router.query.get('apps'));
        router.setQuery({ github_app: null, apps: null }, { replace: true });
      }
      void load();
    });
    return () => clearTimeout(timer);
  });

  const pending = $derived(apps.find((a) => a.state === 'pending'));
  const retired = $derived(apps.filter((a) => a.state === 'retired'));
  const madeApp = $derived(conns[0] ? apps.find((a) => a.connection_id === conns[0].id) : undefined);
  // An app made to replace a connection is installed, but moving to it failed: Bothy still uses the old one.
  const switching = $derived(apps.find((a) => a.state === 'connected' && a.replaces && conns.some((c) => c.id === a.replaces)));
  let retrying = $state(false);
  let retried = $state(false);
  let switchFailure = $state('');

  async function switchAgain() {
    if (!switching) return;
    retrying = retried = true;
    switchFailure = '';
    try {
      await api.post(`/api/github/apps/${encodeURIComponent(switching.id)}/check`);
    } catch (e) {
      switchFailure = errorText(e);
    }
    await load();
    retrying = false;
  }

  async function installed() {
    landing = 'installed';
    landed = null;
    replacing = null;
    await load();
  }

  function found(p: GitHubProbe) {
    probe = p;
    // Repositories this or the replaced connection already allows start ticked.
    chosen = p.repos.filter((r) => r.allowed).map((r) => `${r.owner}/${r.name}`);
  }

  async function test(body: Record<string, unknown>, app: boolean): Promise<GitHubOutcome> {
    let p: GitHubProbe | null = null;
    let err: unknown;
    try {
      p = await api.post<GitHubProbe>('/api/github/connections/test', body);
    } catch (e) {
      err = e;
    }
    const o = githubOutcome(p, err, app);
    if (o.ok && p) found(p);
    else probe = app && p ? { ...p, repos: [] } : null;
    return o;
  }

  async function checkToken(v: string, signal: AbortSignal): Promise<CheckResult> {
    probe = null;
    const o = await test({ auth: 'pat', token: v }, false);
    if (signal.aborted) return { ok: false, message: '' };
    return o.ok ? o : { ok: false, message: o.message, details: o.details };
  }

  function appChanged() {
    clearTimeout(timer);
    appResult = null;
    probe = null;
    if (appId.trim() && key.includes('PRIVATE KEY')) timer = setTimeout(checkApp, 800);
  }

  async function checkApp() {
    const mine = ++run;
    appChecking = true;
    const o = await test({ auth: 'app', app_id: appId.trim(), private_key: key, installation_id: Number(installation) || undefined }, true);
    if (mine !== run) return;
    appResult = o;
    appChecking = false;
  }

  async function readKey(e: Event) {
    const f = (e.currentTarget as HTMLInputElement).files?.[0];
    if (!f) return;
    key = await f.text();
    appChanged();
  }

  async function saveAddress() {
    const v = address.trim();
    if (v === config.value<string>('github.api_base', '')) return;
    addressStatus = await saveConfig({ 'github.api_base': v });
  }

  async function save() {
    saving = true;
    failure = null;
    const body =
      auth === 'pat'
        ? { auth, token: token.trim(), repos: chosen }
        : { auth, app_id: appId.trim(), private_key: key, installation_id: Number(installation) || probe?.installation_id, repos: chosen };
    try {
      // GitHub lets a repository belong to one connection, so the old one goes first.
      if (replacing) await api.del(`/api/github/connections/${encodeURIComponent(replacing.id)}`);
      await api.post('/api/github/connections', body);
      toast('Saved.');
      reading = chosen.length;
      token = key = '';
      probe = null;
    } catch (e) {
      failure = { message: "Bothy couldn't save this connection. Try again.", details: errorText(e) };
    } finally {
      replacing = null;
      saving = false;
      await load();
    }
  }

  function replace() {
    replacing = conns[0];
    auth = conns[0].auth === 'app' ? 'app' : 'pat';
    appId = conns[0].app_id ?? '';
    probe = null;
    landing = null;
    view = 'connect';
  }

  function cancelReplace() {
    replacing = null;
    void load();
  }

  const locked = $derived(!!config.entry('github.api_base')?.locked);
  const installs = $derived(probe?.installations ?? []);
</script>

{#snippet notices(manage: boolean)}
  {#if landed}
    <div class={manage ? 'mb-6' : 'mt-6'}>
      <StatusLine
        tone="danger"
        alert
        text={landed.message}
        action={landed.unlock ? { label: 'Unlock saved keys', onclick: () => router.navigate('/settings/security/keys') } : undefined}
      />
      {#if landed.link}
        <p class="mt-1 pl-6 text-small">
          <a class="link -my-0.5 inline-flex min-h-6 items-center pointer-coarse:-my-3 pointer-coarse:min-h-11" href={landed.link} target="_blank" rel="noopener noreferrer"
            >GitHub Apps<span aria-hidden="true">&nbsp;↗</span><span class="sr-only"> (opens in a new tab)</span></a
          >
        </p>
      {/if}
    </div>
  {/if}
  {#if retired.length}
    <div class={manage ? 'mb-6' : 'mt-6'}>
      {#each retired as a (a.id)}<GitHubAppRetired app={a} ondismissed={load} />{/each}
    </div>
  {/if}
  {#if manage && switching}
    <div class="mb-6">
      {#if retrying}
        <StatusLine tone="progress" text="Switching to {switching.name}…" />
      {:else}
        <StatusLine
          tone="danger"
          alert={retried}
          text="Bothy couldn't switch to {switching.name} yet, so it still uses this connection."
          details={switchFailure || undefined}
          action={{ label: 'Try again', onclick: switchAgain }}
        />
      {/if}
    </div>
  {/if}
  {#if manage && pending}
    <GitHubAppPending app={pending} compact requested={landing === 'requested'} failed={landing === 'install_failed'} oninstalled={installed} onretired={load} />
  {/if}
{/snippet}

{#if view === 'manage' && conns[0]}
  <GitHubManage
    conn={conns[0]}
    app={madeApp}
    landing={!switching && (landing === 'installed' || landing === 'updated') ? landing : undefined}
    {reading}
    onchanged={load}
    onreplace={replace}
  >
    {#snippet top()}{@render notices(true)}{/snippet}
  </GitHubManage>
{:else}
  <ConnPage title="Connect GitHub">
    {#if view === 'failed'}
      <EmptyState text="This page didn't load. Check your connection and try again." action={{ label: 'Try again', onclick: load, variant: 'secondary' }} />
    {:else if view === 'connect'}
      <p class="text-reading text-text">Bothy uses this to read your code and open pull requests. It only ever works on its own branches.</p>
      {@render notices(false)}

      {#if pending}
        <GitHubAppPending app={pending} requested={landing === 'requested'} failed={landing === 'install_failed'} oninstalled={installed} onretired={load} />
      {:else}
        <GitHubCreateApp username={session.me?.user.username ?? ''} taken={apps.map((a) => a.name)} {replacing} oncancel={cancelReplace} onpending={load} />
      {/if}

      <Disclosure class="mt-10" variant="section" label="Other ways to connect">
        <h2 class="section-title">How do you want to connect?</h2>
        <OptionList
          class="mt-2"
          name="github-auth"
          label="How do you want to connect?"
          bind:value={auth}
          onchange={() => (probe = null)}
          options={[
            { value: 'pat', title: 'Personal access token', description: 'Quickest. Acts as the person who created the token.' },
            { value: 'app', title: 'A GitHub App you already have', description: 'Paste its App ID and private key.' },
          ]}
        >
          {#snippet expanded(v)}
            {#if v === 'pat'}
              <KeyField label="Token" bind:value={token} check={checkToken} placeholder="github_pat_…" checkingLabel="Checking the token…" />
              <div class="mt-2 flex flex-wrap items-baseline gap-x-4 gap-y-1 text-small text-muted">
                <a
                  class="link -my-0.5 inline-flex min-h-6 items-center pointer-coarse:-my-3 pointer-coarse:min-h-11"
                  href="https://github.com/settings/personal-access-tokens/new"
                  target="_blank"
                  rel="noopener noreferrer"
                  >Create a fine-grained token on GitHub<span aria-hidden="true">&nbsp;↗</span><span class="sr-only"> (opens in a new tab)</span></a
                >
                <Disclosure variant="inline" label="Which permissions?">
                  <span class="block text-text">Contents, Pull requests, Issues and Actions: read and write. Actions is for deploys.</span>
                  <span class="block text-text">Metadata: read.</span>
                </Disclosure>
              </div>
            {:else}
              <div class="space-y-4">
                <TextField label="App ID" bind:value={appId} inputmode="numeric" autocomplete="off" oninput={appChanged} />
                {#if paste}
                  <TextArea label="Private key" code rows={4} bind:value={key} oninput={appChanged} placeholder="-----BEGIN RSA PRIVATE KEY-----" spellcheck="false">
                    {#snippet labelAction()}<LinkButton onclick={() => (paste = false)}>Choose a file instead</LinkButton>{/snippet}
                  </TextArea>
                {:else}
                  <div>
                    <div class="flex items-baseline justify-between gap-3">
                      <label class="field-label" for="gh-key">Private key</label>
                      <span class="text-small"><LinkButton onclick={() => (paste = true)}>Paste instead</LinkButton></span>
                    </div>
                    <input
                      id="gh-key"
                      type="file"
                      accept=".pem"
                      class="mt-2 block w-full text-body text-text file:mr-3 file:h-9 file:cursor-pointer file:rounded-md file:border file:border-border-strong file:bg-surface file:px-4 file:text-body file:font-medium file:text-text pointer-coarse:file:h-11"
                      onchange={readKey}
                    />
                    <p class="field-help">The .pem file GitHub gave you when you created the app.</p>
                  </div>
                {/if}
                {#if installs.length > 1}
                  <Select
                    label="Organisation"
                    bind:value={installation}
                    onchange={checkApp}
                    options={[{ value: '', label: 'Choose where the app is installed' }, ...installs.map((i) => ({ value: String(i.id), label: i.account }))]}
                  />
                {/if}
                {#if appChecking}
                  <StatusLine tone="progress" text="Checking the app…" />
                {:else if appResult?.ok}
                  <StatusLine tone="success" text={appResult.message} />
                {:else if appResult && !(installs.length > 1 && !installation)}
                  <StatusLine tone="danger" alert text={appResult.message} details={appResult.details} />
                  {#if appResult.notInstalled && probe?.app_slug}
                    <Button href="https://github.com/apps/{probe.app_slug}/installations/new" external>Install on GitHub</Button>
                  {/if}
                {/if}
              </div>
            {/if}
          {/snippet}
        </OptionList>

        {#if probe?.repos.length}
          <h2 class="mt-10 section-title">Which repositories can Bothy use?</h2>
          <div class="mt-2"><RepoPicker repos={probe.repos.filter((r) => !r.archived)} bind:selected={chosen} /></div>
          <div class="mt-6 flex flex-wrap items-center gap-x-3 gap-y-2">
            <Button
              variant="primary"
              class="max-sm:w-full"
              loading={saving}
              loadingLabel="Saving…"
              onclick={save}
              reason={chosen.length ? undefined : 'Choose at least one repository.'}>Save</Button
            >
          </div>
        {/if}
        {#if failure}<StatusLine class="mt-3" tone="danger" alert text={failure.message} details={failure.details} />{/if}
      </Disclosure>

      <Disclosure class="mt-4" variant="section" label="Using GitHub Enterprise?">
        <div class="max-w-sm">
          {#if locked}
            <TextField label="GitHub address" readonly value={address} />
            <p class="field-help">The server's settings set this address. Change it there.</p>
          {:else}
            <TextField
              label="GitHub address"
              type="url"
              bind:value={address}
              placeholder="https://github.acme.com/api/v3"
              error={addressStatus?.message}
              onblur={saveAddress}
              onkeydown={(e: KeyboardEvent) => e.key === 'Enter' && saveAddress()}
            />
          {/if}
        </div>
      </Disclosure>
    {:else}
      <Loading />
    {/if}
  </ConnPage>
{/if}
