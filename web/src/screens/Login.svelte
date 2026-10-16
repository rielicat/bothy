<script lang="ts">
  import { onMount } from 'svelte';
  import Icon from '../components/Icon.svelte';
  import Button from '../components/ui/Button.svelte';
  import LinkButton from '../components/ui/LinkButton.svelte';
  import Notice from '../components/ui/Notice.svelte';
  import TextField from '../components/ui/TextField.svelte';
  import { api, ApiError } from '../lib/api/client';
  import type { Me, SignInMethods, SignInOption, SignInProvider, SignInStartResp } from '../lib/api/types.gen';
  import { away } from '../lib/away';
  import { router } from '../lib/router.svelte';
  import { session } from '../lib/stores/session.svelte';
  import { loginError, rememberOneTime, signInErrorText, takeRefused, unanswered } from './setup/auth';
  import Frame from './setup/Frame.svelte';
  import PasswordField from './setup/PasswordField.svelte';
  import RemoteSetup from './setup/RemoteSetup.svelte';

  // Sign in (docs/design/ui.md §5.9).
  let username = $state('');
  let password = $state('');
  let busy = $state(false);
  let error = $state('');
  let remote = $state<{ fix?: string } | null>(null);
  let userInput: HTMLInputElement | undefined = $state();
  let passInput: HTMLInputElement | undefined = $state();

  // Google and GitHub, when an admin turned them on (§5.9, SPEC §15).
  let providers = $state<SignInOption[]>([]);
  let opening = $state<SignInProvider | null>(null);
  let providerError = $state('');
  // A start from an address other than Bothy's web address: where to go instead.
  let elsewhere = $state('');

  const next = $derived(router.query.get('next'));
  const notice = $derived(
    providerError
      ? ''
      : session.setup?.needs_setup
        ? 'Sign in to finish setting up Bothy.'
        : router.query.has('expired') || (next && next !== '/setup')
          ? 'You were signed out. Sign in again to continue.'
          : '',
  );

  // Setup routes answer other computers with 403, which leaves the app
  // without a setup state; then setup has to be finished on the server (§5.4).
  onMount(() => {
    // A provider round trip that failed comes back here with its reason. The
    // sentence stays; the address is cleaned so a reload doesn't repeat it.
    const q = router.query;
    const code = q.get('signin_error');
    if (code) {
      providerError = signInErrorText(code, q.get('provider'), code === 'not_allowed' ? takeRefused() : '');
      const n = q.get('next');
      router.navigate('/login' + (n ? `?next=${encodeURIComponent(n)}` : ''), { replace: true });
    }
    api.get<SignInMethods>('/api/auth/methods').then(
      (m) => (providers = m.providers ?? []),
      () => {},
    );
    if (session.setup) return;
    api.get('/api/setup/state').catch((e) => {
      if (e instanceof ApiError && e.status === 403) remote = { fix: e.fix };
    });
  });

  function startError(e: unknown, p: SignInOption): string {
    if (unanswered(e)) return "Bothy didn't respond. Check your connection, then try again.";
    const err = e as ApiError;
    if (err.status === 429) return loginError(e);
    if (err.code === 'wrong_address' && err.fix) {
      try {
        const u = new URL(err.fix);
        elsewhere = u.origin;
        return `Open Bothy at ${u.host} to sign in with ${p.name}.`;
      } catch {
        // an unreadable address falls through to the general sentence
      }
    }
    return signInErrorText('unavailable', p.provider);
  }

  async function start(p: SignInOption) {
    if (opening) return;
    opening = p.provider;
    providerError = '';
    elsewhere = '';
    error = '';
    try {
      const r = await api.post<SignInStartResp>(`/api/auth/oauth/${p.provider}/start`, next ? { next } : {});
      // The button stays busy while the browser leaves for the provider.
      away.to(r.redirect_url);
    } catch (e) {
      opening = null;
      providerError = startError(e, p);
    }
  }

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    if (!username.trim()) return userInput?.focus();
    if (!password) return passInput?.focus();
    busy = true;
    error = '';
    providerError = '';
    try {
      const me = await api.post<Me>('/api/auth/login', { username: username.trim(), password });
      // An account an admin just created signs in with a one-time password (S9).
      if (me.user.must_change_password) {
        rememberOneTime(password);
        router.navigate(next ? `/password?next=${encodeURIComponent(next)}` : '/password', { replace: true });
      }
      session.login(me);
    } catch (err) {
      error = loginError(err);
      password = '';
      passInput?.focus();
    } finally {
      busy = false;
    }
  }
</script>

{#snippet failure(cls: string)}
  <p class="field-error {cls}" role="alert">
    <Icon name="alert" class="mt-px" /><span class="min-w-0"
      >{providerError}{#if elsewhere}{' '}<LinkButton onclick={() => away.to(`${elsewhere}/login`)}>Go there</LinkButton>{/if}</span
    >
  </p>
{/snippet}

{#if remote}
  <RemoteSetup fix={remote.fix} />
{:else}
  <Frame title="Sign in">
    {#if notice}<Notice class="mb-6" text={notice} />{/if}
    {#if providerError}{@render failure('mb-4')}{/if}
    {#if providers.length}
      <div class="flex flex-col items-start gap-3">
        {#each providers as p (p.provider)}
          <Button class="h-10 max-sm:w-full pointer-coarse:h-11" loading={opening === p.provider} loadingLabel="Opening {p.name}…" onclick={() => start(p)}
            >Continue with {p.name}</Button
          >
        {/each}
      </div>
      <div class="my-6 flex items-center gap-3 text-small text-muted">
        <span class="h-px flex-1 bg-border" aria-hidden="true"></span>or<span class="h-px flex-1 bg-border" aria-hidden="true"></span>
      </div>
    {/if}
    <form novalidate onsubmit={submit}>
      <div class="space-y-5">
        <TextField label="Username" bind:value={username} bind:input={userInput} autocomplete="username" autocapitalize="none" spellcheck="false" />
        <PasswordField label="Password" bind:value={password} bind:input={passInput} autocomplete="current-password" />
      </div>
      {#if error}
        <p class="field-error mt-8" role="alert"><Icon name="alert" class="mt-px" /><span class="min-w-0">{error}</span></p>
      {/if}
      <Button type="submit" variant="primary" class="{error ? 'mt-4' : 'mt-8'} h-10 max-sm:w-full pointer-coarse:h-11" loading={busy} loadingLabel="Signing in…">Sign in</Button>
    </form>
  </Frame>
{/if}
