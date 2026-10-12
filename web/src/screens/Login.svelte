<script lang="ts">
  import Icon from '../components/Icon.svelte';
  import { api, errorText } from '../lib/api/client';
  import type { Me } from '../lib/api/types.gen';
  import { session } from '../lib/stores/session.svelte';

  let username = $state('');
  let password = $state('');
  let show = $state(false);
  let busy = $state(false);
  let error = $state('');

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    busy = true;
    error = '';
    try {
      session.login(await api.post<Me>('/api/auth/login', { username, password }));
    } catch (err) {
      error = errorText(err);
      password = '';
    } finally {
      busy = false;
    }
  }
</script>

<main class="flex min-h-dvh flex-col items-center justify-center bg-bg px-4 py-10">
  <div class="w-full max-w-sm">
    <div class="mb-8 flex flex-col items-center text-center">
      <span class="mb-4 grid size-12 place-items-center rounded-2xl bg-accent text-on-accent shadow-sm"><Icon name="bothy" size={26} /></span>
      <h1 class="text-2xl font-semibold tracking-tight">Welcome back</h1>
      <p class="mt-1 text-sm text-muted">
        {session.setup?.needs_setup ? 'Log in to finish setting up Bothy.' : 'Log in to your Bothy.'}
      </p>
    </div>
    <form class="card space-y-4 p-6 shadow-sm" onsubmit={submit}>
      <div>
        <label class="label" for="login-user">Username</label>
        <input id="login-user" class="input" autocomplete="username" autocapitalize="none" spellcheck="false" required bind:value={username} />
      </div>
      <div>
        <label class="label" for="login-pass">Password</label>
        <div class="relative">
          <input
            id="login-pass"
            class="input pr-10"
            type={show ? 'text' : 'password'}
            autocomplete="current-password"
            required
            bind:value={password}
          />
          <button
            type="button"
            class="absolute inset-y-0 right-0 grid w-10 place-items-center text-muted hover:text-fg"
            aria-label={show ? 'Hide password' : 'Show password'}
            aria-pressed={show}
            onclick={() => (show = !show)}><Icon name={show ? 'eyeOff' : 'eye'} size={17} /></button
          >
        </div>
      </div>
      {#if error}
        <p class="alert-bad" role="alert">{error}</p>
      {/if}
      <button class="btn btn-primary w-full" disabled={busy}>{busy ? 'Logging in…' : 'Log in'}</button>
    </form>
    <p class="mt-6 text-center text-xs text-muted">
      Forgot your password? Ask an admin, or reset it on the host with <code class="rounded bg-sunken px-1">bothy user</code>.
    </p>
  </div>
</main>
