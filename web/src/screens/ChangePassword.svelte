<script lang="ts">
  import Icon from '../components/Icon.svelte';
  import Button from '../components/ui/Button.svelte';
  import TextField from '../components/ui/TextField.svelte';
  import { api, ApiError } from '../lib/api/client';
  import { passwordError, passwordHelp } from '../lib/password';
  import { router, safeNext } from '../lib/router.svelte';
  import { session } from '../lib/stores/session.svelte';
  import { loginError, takeOneTime, unanswered } from './setup/auth';
  import Frame from './setup/Frame.svelte';
  import PasswordField from './setup/PasswordField.svelte';

  // The first sign-in after an admin created the account (docs/design/ui.md
  // §5.9, S9). The one-time password comes from sign in; after a reload it is
  // asked for again.
  const remembered = takeOneTime();
  let old = $state(remembered);
  let password = $state('');
  let busy = $state(false);
  let submitted = $state(false);
  let error = $state('');
  let oldError = $state('');
  let server = $state('');
  let input: HTMLInputElement | undefined = $state();
  let oldInput: HTMLInputElement | undefined = $state();

  const fieldError = $derived(server || (submitted ? passwordError(password) : ''));

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    submitted = true;
    error = '';
    oldError = !remembered && !old ? 'Enter the one-time password you signed in with.' : '';
    if (oldError) return oldInput?.focus();
    if (fieldError) return input?.focus();
    busy = true;
    try {
      await api.post('/api/auth/password', { old, new: password });
      // The server rotated the session, and with it the CSRF token.
      await session.refresh();
      router.navigate(safeNext(router.query.get('next')), { replace: true });
    } catch (err) {
      const m = err instanceof ApiError ? err.message.toLowerCase() : '';
      if (unanswered(err)) error = "Bothy didn't respond. Check your connection, then try again.";
      // Wrong one-time passwords typed after a reload count toward the lockout.
      else if ((err as ApiError).status === 429) error = loginError(err);
      else if (m.includes('common') || m.includes('predictable')) server = 'This password is too common. Try a phrase of a few words.';
      // "not the one-time password", or "must differ from the current one" when it was typed again after a reload.
      else if (m.includes('one-time') || m.includes('differ')) server = 'Choose a new password, not the one-time password.';
      else if ((err as ApiError).status === 401 || m.includes('old') || m.includes('current')) {
        oldError = "That one-time password didn't work. Ask an admin for a new one.";
        old = remembered ? '' : old;
      } else server = 'Use at least 12 characters.';
      (server ? input : oldInput)?.focus();
    } finally {
      busy = false;
    }
  }
</script>

<Frame title="Choose a new password" intro="You signed in with a one-time password. Choose your own to continue." card>
  <form novalidate onsubmit={submit}>
    <div class="space-y-5">
      {#if !remembered || oldError}
        <TextField label="One-time password" type="password" bind:value={old} bind:input={oldInput} error={oldError} autocomplete="current-password" oninput={() => (oldError = '')} />
      {/if}
      <PasswordField label="New password" bind:value={password} bind:input error={fieldError} help={passwordHelp(password)} autocomplete="new-password" oninput={() => (server = '')} />
    </div>
    {#if error}
      <p class="field-error mt-8" role="alert"><Icon name="alert" class="mt-px" /><span class="min-w-0">{error}</span></p>
    {/if}
    <Button type="submit" variant="primary" class="{error ? 'mt-4' : 'mt-8'} h-10 max-sm:w-full pointer-coarse:h-11" loading={busy} loadingLabel="Saving…">Save and continue</Button>
  </form>
</Frame>
