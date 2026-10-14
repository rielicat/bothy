<script lang="ts">
  import { onMount } from 'svelte';
  import Icon from '../components/Icon.svelte';
  import Button from '../components/ui/Button.svelte';
  import Notice from '../components/ui/Notice.svelte';
  import TextField from '../components/ui/TextField.svelte';
  import { api, ApiError } from '../lib/api/client';
  import { router } from '../lib/router.svelte';
  import { session } from '../lib/stores/session.svelte';
  import { loginError, rememberOneTime, type MeV2 } from './setup/auth';
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

  const next = $derived(router.query.get('next'));
  const notice = $derived(
    session.setup?.needs_setup
      ? 'Sign in to finish setting up Bothy.'
      : router.query.has('expired') || (next && next !== '/setup')
        ? 'You were signed out. Sign in again to continue.'
        : '',
  );

  // Setup routes answer other computers with 403, which leaves the app
  // without a setup state; then setup has to be finished on the server (§5.4).
  onMount(() => {
    if (session.setup) return;
    api.get('/api/setup/state').catch((e) => {
      if (e instanceof ApiError && e.status === 403) remote = { fix: e.fix };
    });
  });

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    if (!username.trim()) return userInput?.focus();
    if (!password) return passInput?.focus();
    busy = true;
    error = '';
    try {
      const me = await api.post<MeV2>('/api/auth/login', { username: username.trim(), password });
      // An account an admin just created signs in with a one-time password (S9).
      if (me.must_change_password) {
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

{#if remote}
  <RemoteSetup fix={remote.fix} />
{:else}
  <Frame title="Sign in">
    {#if notice}<Notice class="mb-6" text={notice} />{/if}
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
