<script lang="ts" module>
  export interface Account {
    name: string;
    username: string;
    password: string;
  }
</script>

<script lang="ts">
  import { tick } from 'svelte';
  import Icon from '../../components/Icon.svelte';
  import Button from '../../components/ui/Button.svelte';
  import TextField from '../../components/ui/TextField.svelte';
  import { api, setCsrf } from '../../lib/api/client';
  import type { Me } from '../../lib/api/types.gen';
  import { nameError, passwordError, passwordHelp, usernameError, usernameFrom } from '../../lib/password';
  import { session } from '../../lib/stores/session.svelte';
  import { browserZone, ownerError, type AccountField } from './auth';
  import Frame from './Frame.svelte';
  import PasswordField from './PasswordField.svelte';

  // Step 1 (docs/design/ui.md §5.5). The values live in the parent so they
  // survive a trip back to the setup code when the grant times out.
  let { account = $bindable(), onexpired, oncreated }: { account: Account; onexpired: (text: string) => void; oncreated: () => void } = $props();

  let submitted = $state(false);
  let busy = $state(false);
  let formError = $state('');
  let server = $state<{ field: AccountField; text: string } | null>(null);
  const inputs: Partial<Record<AccountField, HTMLInputElement>> = $state({});

  // Errors show after the first submit, then follow the typing (§15.3 row 4).
  const errors = $derived({
    name: (server?.field === 'name' && server.text) || (submitted ? nameError(account.name) : ''),
    username: (server?.field === 'username' && server.text) || (submitted ? usernameError(account.username) : ''),
    password: (server?.field === 'password' && server.text) || (submitted ? passwordError(account.password) : ''),
  });

  // The live region trails the help by 500 ms, so it doesn't speak every keystroke.
  let spoken = $state('');
  let speak: ReturnType<typeof setTimeout> | undefined;
  $effect(() => {
    const text = passwordHelp(account.password);
    clearTimeout(speak);
    speak = setTimeout(() => (spoken = account.password ? text : ''), 500);
    return () => clearTimeout(speak);
  });

  function edit(field: AccountField) {
    if (server?.field === field) server = null;
  }

  // The username follows the name until the person types one of their own.
  let ownUsername = !!account.username && account.username !== usernameFrom(account.name);
  function nameTyped() {
    edit('name');
    if (!ownUsername) account.username = usernameFrom(account.name);
  }
  function usernameTyped() {
    edit('username');
    ownUsername = !!account.username;
  }

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    submitted = true;
    formError = '';
    const first = (['name', 'username', 'password'] as const).find((f) => errors[f]);
    if (first) return inputs[first]?.focus();
    busy = true;
    const name = account.name.trim();
    const timezone = browserZone();
    try {
      let me = await api.post<Me>('/api/setup/owner', { display_name: name, username: account.username.trim(), password: account.password, timezone });
      // The owner's session has its own CSRF token; the setup grant's no longer passes.
      setCsrf(me.csrf);
      // Silent defaults (§5.8): team mode and the browser's time zone, and the
      // owner's name where the server doesn't take it at creation yet (S7).
      await api.patch('/api/config', { set: { mode: 'multi', timezone } }).catch(() => {});
      if (me.user.display_name !== name) {
        await api.patch(`/api/users/${encodeURIComponent(me.user.id)}`, { display_name: name }).then(
          () => (me = { ...me, user: { ...me.user, display_name: name } }),
          () => {},
        );
      }
      session.login(me);
      if (session.setup) session.setup.owner_set = true;
      account.password = '';
      oncreated();
    } catch (err) {
      const r = ownerError(err);
      if (r.expired) return onexpired(r.text);
      if (r.field) {
        server = { field: r.field, text: r.text };
        await tick();
        inputs[r.field]?.focus();
      } else formError = r.text;
    } finally {
      busy = false;
    }
  }
</script>

<Frame step={1} title="Create your account" intro="You'll be the owner of this Bothy. You can add your team when you're done.">
  <form novalidate onsubmit={submit}>
    <div class="space-y-5">
      <TextField label="Your name" bind:value={account.name} bind:input={inputs.name} error={errors.name} autocomplete="name" maxlength={64} oninput={nameTyped} />
      <TextField
        label="Username"
        bind:value={account.username}
        bind:input={inputs.username}
        error={errors.username}
        help="You'll use this to sign in."
        autocomplete="username"
        autocapitalize="none"
        spellcheck="false"
        oninput={usernameTyped}
      />
      <PasswordField
        label="Password"
        bind:value={account.password}
        bind:input={inputs.password}
        error={errors.password}
        help={passwordHelp(account.password)}
        autocomplete="new-password"
        oninput={() => edit('password')}
      />
      <span class="sr-only" aria-live="polite">{spoken}</span>
    </div>
    {#if formError}
      <p class="field-error mt-8" role="alert"><Icon name="alert" class="mt-px" /><span class="min-w-0">{formError}</span></p>
    {/if}
    <Button type="submit" variant="primary" class="{formError ? 'mt-4' : 'mt-8'} h-10 max-sm:w-full pointer-coarse:h-11" loading={busy} loadingLabel="Creating account…">Create account</Button>
  </form>
</Frame>
