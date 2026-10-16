<script lang="ts">
  import { Button, Disclosure, OptionList, StatusLine, TextField } from '../../../components/ui';
  import { api } from '../../../lib/api/client';
  import { away } from '../../../lib/away';
  import { router } from '../../../lib/router.svelte';
  import { appStartFailure, defaultAppName, MAX_APP_NAME, orgPattern, type AppStartFailure } from './connections';
  import type { GitHubAppStartResp, GitHubConnection } from './types';

  // The primary way to connect (§12.2): Bothy has GitHub create an app of
  // its own, which pull requests and comments then come from.
  let {
    username = '',
    replacing = null,
    oncancel,
    onpending,
  }: {
    /** Names a personal account's app, as the server does. */
    username?: string;
    /** The connection the new app replaces once it is installed. */
    replacing?: GitHubConnection | null;
    oncancel?: () => void;
    /** Another app is waiting to be installed: the page shows it instead. */
    onpending: () => void;
  } = $props();

  let account = $state('organization');
  let org = $state('');
  let name = $state('');
  let touched = $state(false);
  let starting = $state(false);
  let failure = $state<AppStartFailure | null>(null);

  const orgValue = $derived(org.trim());
  const orgError = $derived(
    account === 'organization' && touched && orgValue && !orgPattern.test(orgValue)
      ? "That isn't an organization name on GitHub. Use the name from its address, like acme in github.com/acme."
      : undefined,
  );
  const nameError = $derived([...name.trim()].length > MAX_APP_NAME ? `Use ${MAX_APP_NAME} characters or fewer.` : undefined);
  const proposed = $derived(defaultAppName(account === 'organization' ? orgValue || username : username));
  const reason = $derived(account === 'organization' && !orgValue ? "Enter the organization's name." : undefined);

  async function start() {
    touched = true;
    if ((account === 'organization' && !orgPattern.test(orgValue)) || nameError) return;
    starting = true;
    failure = null;
    const body: Record<string, string> = { account };
    if (account === 'organization') body.org = orgValue;
    if (name.trim()) body.name = name.trim();
    if (replacing) body.replaces = replacing.id;
    try {
      const r = await api.post<GitHubAppStartResp>('/api/github/apps', body);
      // The button keeps saying "Opening GitHub…" while the browser leaves.
      away.to(r.handoff_url);
    } catch (e) {
      starting = false;
      const f = appStartFailure(e);
      if (f.pending) onpending();
      else failure = f;
    }
  }

  function failureAction(f: AppStartFailure) {
    if (f.fix) return { label: `Open ${f.fix}`, onclick: () => away.to(`${f.fix}/settings/connections/github`) };
    if (f.unlock) return { label: 'Unlock saved keys', onclick: () => router.navigate('/settings/security/keys') };
    return undefined;
  }
</script>

<h2 class="mt-10 section-title">Create a GitHub App for Bothy</h2>
<p class="mt-1 text-body text-muted">Pull requests and comments come from Bothy itself, not from someone's account. You choose which repositories it can use.</p>

<p class="mt-6 field-label">Where will Bothy work?</p>
<OptionList
  class="mt-2"
  name="github-owner"
  label="Where will Bothy work?"
  bind:value={account}
  onchange={() => (failure = null)}
  options={[
    { value: 'organization', title: 'An organization' },
    { value: 'personal', title: 'Your personal account', description: 'For repositories your own GitHub account owns.' },
  ]}
>
  {#snippet expanded(v)}
    {#if v === 'organization'}
      <div class="max-w-sm">
        <TextField
          label="Organization"
          bind:value={org}
          placeholder="acme"
          autocomplete="off"
          spellcheck="false"
          help="You need to be an owner of this organization on GitHub."
          error={orgError}
          onblur={() => (touched = true)}
          onkeydown={(e: KeyboardEvent) => e.key === 'Enter' && start()}
        />
      </div>
    {/if}
  {/snippet}
</OptionList>

<div class="mt-3 flex flex-col items-start gap-2 text-small text-muted">
  <Disclosure variant="inline" label="Change the app's name">
    <div class="max-w-sm text-body">
      <TextField label="App name" bind:value={name} placeholder={proposed} error={nameError} help="Names are unique on GitHub. You can also change it on the next page." />
    </div>
  </Disclosure>
  <Disclosure variant="inline" label="What can the app do?">
    <span class="block text-text">Contents, pull requests, issues and Actions: read and write. Actions is for deploys.</span>
    <span class="block text-text">Metadata: read.</span>
    <span class="block text-text">It can't change repository settings, secrets or workflow files.</span>
  </Disclosure>
</div>

<div class="mt-6 flex flex-wrap items-center gap-x-3 gap-y-2">
  <Button variant="primary" class="max-sm:w-full" loading={starting} loadingLabel="Opening GitHub…" {reason} onclick={start}>Continue on GitHub</Button>
  {#if replacing && oncancel}<Button onclick={oncancel}>Cancel</Button>{/if}
</div>
<p class="mt-2 text-small text-muted">GitHub opens in this tab. Sign in there if asked, then create the app and install it.</p>
{#if failure}<StatusLine class="mt-3" tone="danger" alert text={failure.message} details={failure.details} action={failureAction(failure)} />{/if}
