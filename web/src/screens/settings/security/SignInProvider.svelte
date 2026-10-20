<script lang="ts">
  import { untrack } from 'svelte';
  import { Button, CodeBlock, Disclosure, LinkButton, Notice, Page, SettingsRow, SettingsSection, StatusLine, Switch, TextField } from '../../../components/ui';
  import { api, ApiError } from '../../../lib/api/client';
  import type { SignInAdmin, SignInCheck, SignInProvider } from '../../../lib/api/types.gen';
  import { config } from '../../../lib/config.svelte';
  import { router } from '../../../lib/router.svelte';
  import { session } from '../../../lib/stores/session.svelte';
  import LinkRow from './LinkRow.svelte';
  import { securityBack } from './page.svelte';
  import PageState from './PageState.svelte';
  import { addressSummary, canEnable, checkLine, hasRules, listSummary, orgsProblem, parseOrgs, providerNames } from './signin';

  // Google sign-in and GitHub sign-in (docs/design/ui.md §11.6). The client
  // ID, secret and redirect address are what admins paste between Bothy and
  // the provider's console, so they are named as the console names them.
  let { provider }: { provider: SignInProvider } = $props();

  const FIXED_HELP = "This is set on the server and can't be changed here.";
  const name = $derived(providerNames[provider]);
  const github = $derived(provider === 'github');
  const base = $derived(`auth.sign_in.${provider}`);

  let adm = $state<SignInAdmin | null>(null);
  let failed = $state(false);
  let clientId = $state('');
  let secret = $state('');
  let orgsText = $state('');
  let replacing = $state(false);
  let saving = $state(false);
  let checking = $state(false);
  let result = $state<{ tone: 'success' | 'danger' | 'warning'; text: string; details?: string } | null>(null);
  let errors = $state<{ clientId?: string; secret?: string; orgs?: string }>({});
  let switchRow = $state<{ status: 'saving' | 'saved' | null; error?: string }>({ status: null });

  const st = $derived(adm?.providers.find((p) => p.provider === provider));
  const fixed = (path: string) => {
    const e = config.entry(path);
    return !!e && (e.locked || e.source.layer === 'env');
  };

  function fill(a: SignInAdmin) {
    adm = a;
    const s = a.providers.find((p) => p.provider === provider);
    clientId = s?.client_id ?? '';
    orgsText = (s?.orgs ?? []).join(', ');
  }

  function load() {
    failed = false;
    void config.load(true);
    api.get<SignInAdmin>('/api/sign-in').then(fill, () => (failed = true));
  }

  $effect(() => {
    if (session.isAdmin) untrack(load);
  });

  const uid = $props.id();
  // The web address can't carry sign-in: the notice at the top says so and what to do.
  const noticed = $derived(!!adm && adm.address !== 'ok');
  // Why the switch can't be turned on yet; nothing once it is on, so it can always be turned off.
  const reason = $derived.by(() => {
    if (!adm || !st || st.enabled) return '';
    if (fixed(`${base}.enabled`)) return FIXED_HELP;
    if (canEnable(adm, st)) return '';
    if (adm.address === 'not_https') return "Bothy's web address needs to start with https://.";
    if (adm.address !== 'ok') return "Set Bothy's web address first.";
    if (!st.client_id || !st.secret_saved) return 'Save the details below to turn this on.';
    if (!hasRules(adm, st)) return github ? 'Add an email address on the Security page, or an organization below, to turn this on.' : 'Add who can sign in on the Security page to turn this on.';
    return '';
  });

  async function toggle(on: boolean) {
    switchRow = { status: 'saving' };
    try {
      adm = await api.patch<SignInAdmin>(`/api/sign-in/${provider}`, { enabled: on });
      switchRow = { status: 'saved' };
    } catch {
      switchRow = { status: null, error: on ? `Couldn't turn it on. Save the client ID and client secret, and add who can sign in, first.` : "Couldn't save. Try again." };
    }
  }

  // A sentence that says what to do on its own, or the server's words for Details.
  function saveErrors(e: unknown): { plain?: string; details: string } {
    const fields = e instanceof ApiError ? e.fields : [];
    const next: typeof errors = {};
    for (const f of fields) {
      if (f.path === `${base}.client_id`) next.clientId = `Enter the client ID as ${github ? 'GitHub' : 'Google Cloud'} shows it.`;
      if (f.path === `${base}.orgs`) {
        const unknown = /GitHub has no organization called ([A-Za-z0-9-]+)\./.exec(f.message);
        next.orgs = unknown ? `GitHub has no organization called ${unknown[1]}.` : 'Enter organization names like acme-inc, separated by commas.';
      }
    }
    errors = next;
    const details = fields.map((f) => f.message).join('\n');
    if (!(e instanceof ApiError)) return { details };
    if (e.status === 503 && e.code === 'sealed') return { plain: "Saved keys are locked, so the client secret can't be saved. Unlock them in Saved keys.", details };
    // Only the GitHub page looks organizations up on GitHub; any other 502 gets the general sentence.
    if (github && e.status === 502 && e.code === 'provider_error') return { plain: "Bothy couldn't reach GitHub to check the organizations. Try again.", details };
    if (github && e.status === 502 && e.code === 'provider_rate_limited') return { plain: 'GitHub is limiting requests from this server right now. Try again in an hour.', details };
    return { details };
  }

  async function save(e: SubmitEvent) {
    e.preventDefault();
    result = null;
    const orgs = parseOrgs(orgsText);
    errors = {};
    if (github && orgsProblem(orgs)) errors.orgs = orgsProblem(orgs);
    if (secret && [...secret].some((c) => c < ' ')) errors.secret = 'Paste the client secret on its own, without spaces or line breaks.';
    if (Object.keys(errors).length) return;
    saving = true;
    // Saving working details for the first time also turns sign-in on: an
    // admin who has just set it up shouldn't need a second step to use it.
    const firstSetup = !!st && !st.enabled && !(st.client_id && st.secret_saved);
    const body: Record<string, unknown> = { client_id: clientId.trim() };
    if (secret.trim()) body.client_secret = secret.trim();
    if (github) body.orgs = orgs;
    try {
      fill(await api.patch<SignInAdmin>(`/api/sign-in/${provider}`, body));
      secret = '';
      replacing = false;
    } catch (err) {
      saving = false;
      const { plain, details } = saveErrors(err);
      result = { tone: 'danger', text: plain ?? "Couldn't save. Try again.", details: plain ? undefined : details || undefined };
      return;
    }
    saving = false;
    if (!st?.client_id || !st.secret_saved) {
      result = { tone: 'success', text: 'Saved.' };
      return;
    }
    checking = true;
    try {
      const r = await api.post<SignInCheck>(`/api/sign-in/${provider}/check`, {});
      result = { ...checkLine(provider, r), details: r.error };
      if (r.ok && firstSetup && adm && st && !fixed(`${base}.enabled`) && canEnable(adm, st)) {
        await toggle(true);
        if (st.enabled) result = { tone: 'success', text: `Saved. ${name} accepted the client ID and secret, and sign-in with ${name} is on.` };
      }
    } catch {
      result = { tone: 'warning', text: `Saved, but Bothy couldn't reach ${name} to check the details.` };
    } finally {
      checking = false;
    }
  }
</script>

<Page title="{name} sign-in" back={securityBack}>
  {#if !session.isAdmin}
    <PageState kind="forbidden" />
  {:else if failed}
    <PageState kind="failed" onretry={load} />
  {:else if !adm || !st}
    <PageState kind="loading" />
  {:else}
    {#if adm.address === 'missing'}
      <Notice class="mb-6" text="Set Bothy's web address before turning this on. {name} sends people back to it after they sign in." action={{ label: 'Open General', onclick: () => router.navigate('/settings/general') }} />
    {:else if adm.address === 'not_https'}
      <Notice class="mb-6" text="Bothy's web address must start with https:// for {name} sign-in. Put Bothy behind HTTPS, then change the address in General." action={{ label: 'Open General', onclick: () => router.navigate('/settings/general') }} />
    {:else if st.problem === 'no_secret'}
      <Notice class="mb-6" tone="danger" text="The saved client secret is missing. Enter it again and save." />
    {:else if st.problem === 'sealed'}
      <Notice class="mb-6" tone="danger" text="Saved keys are locked, so {name} sign-in can't work. Unlock them in Saved keys." action={{ label: 'Open saved keys', onclick: () => router.navigate('/settings/security/keys') }} />
    {/if}

    <div class="list-card">
      <!-- The address notice above says why the switch is off, so the reason isn't printed a second time; screen readers still get it. -->
      <SettingsRow label="Sign in with {name}" inline help={(!noticed && reason) || undefined} status={switchRow.status} error={switchRow.error}>
        {#snippet children(c)}
          <Switch
            id={c.id}
            label="Sign in with {name}"
            hideLabel
            aria-describedby={noticed && reason ? `${uid}-why` : c.describedBy}
            checked={st.enabled}
            disabled={!!reason}
            onchange={toggle}
          />
          {#if noticed && reason}<span id="{uid}-why" class="sr-only">{reason}</span>{/if}
        {/snippet}
      </SettingsRow>
      <LinkRow
        label="Who can sign in"
        value={github ? addressSummary(adm.allow) : listSummary(adm.allow)}
        href="/settings/security"
        help={github ? undefined : 'A domain on the list lets in the Google Workspace accounts on it.'}
      />
    </div>

    <form class="mt-10" novalidate onsubmit={save}>
      <SettingsSection title={github ? 'GitHub OAuth app' : 'Google Cloud'} bare>
        <div class="card space-y-5">
          <div>
            <p class="field-label">{github ? 'Callback URL' : 'Redirect URI'}</p>
            {#if st.redirect_uri && !noticed}
              <CodeBlock class="mt-2" code={st.redirect_uri} wrap />
              <p class="field-help">
                {github ? 'Paste this as the authorization callback URL of a GitHub OAuth app.' : "Add this to the OAuth client's authorized redirect URIs in Google Cloud."}
              </p>
              <Button class="mt-3" size="sm" href={github ? 'https://github.com/settings/applications/new' : 'https://console.cloud.google.com/apis/credentials'} external
                >{github ? 'Open GitHub' : 'Open Google Cloud'}</Button
              >
            {:else}
              <!-- The notice at the top says what to do; this only says what to expect here. -->
              <p class="mt-2 text-body text-muted">Appears once the web address starts with https://.</p>
            {/if}
          </div>
          <TextField
            label="Client ID"
            bind:value={clientId}
            placeholder={github ? 'Ov23li…' : '1234…apps.googleusercontent.com'}
            autocomplete="off"
            spellcheck="false"
            autocapitalize="none"
            disabled={fixed(`${base}.client_id`)}
            help={fixed(`${base}.client_id`) ? FIXED_HELP : undefined}
            error={errors.clientId}
          />
          {#if st.secret_saved && !replacing}
            <div>
              <p class="field-label">Client secret</p>
              <p class="mt-2 text-body text-text">
                Saved{#if !fixed(`${base}.client_secret`)}{' · '}<LinkButton onclick={() => (replacing = true)} aria-label="Replace the client secret">Replace</LinkButton>{/if}
              </p>
              <p class="field-help">{fixed(`${base}.client_secret`) ? FIXED_HELP : `Stored encrypted. Only sent to ${name}.`}</p>
            </div>
          {:else}
            <TextField label="Client secret" type="password" bind:value={secret} autocomplete="off" spellcheck="false" help="Stored encrypted. Only sent to {name}." error={errors.secret} />
          {/if}
          {#if github}
            <div>
              <TextField
                label="Organizations"
                optional
                bind:value={orgsText}
                placeholder="acme-inc"
                autocomplete="off"
                spellcheck="false"
                autocapitalize="none"
                disabled={fixed(`${base}.orgs`)}
                help={fixed(`${base}.orgs`) ? FIXED_HELP : 'Members of these GitHub organizations can also sign in. Other GitHub accounts need their exact email address on the list.'}
                error={errors.orgs}
              />
              <Disclosure class="mt-2">
                <p class="text-small text-muted">
                  If an organization restricts OAuth apps, an owner approves Bothy the first time a member signs in. Someone whose invitation is still pending can't sign in until they accept
                  it.
                </p>
              </Disclosure>
            </div>
          {/if}
          <div class="pt-1">
            <Button type="submit" variant="primary" loading={saving || checking} loadingLabel={checking ? `Checking with ${name}…` : 'Saving…'}>Save</Button>
            {#if result}<StatusLine class="mt-3" tone={result.tone} text={result.text} details={result.details} alert={result.tone === 'danger'} />{/if}
          </div>
        </div>
      </SettingsSection>
    </form>
  {/if}
</Page>
