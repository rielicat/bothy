<script lang="ts">
  import { Button, CodeBlock, Dialog, Notice, Segmented, TextField } from '../../components/ui';
  import { api, ApiError, refusal } from '../../lib/api/client';
  import type { UserCreateResp } from '../../lib/api/types.gen';
  import { usernameError, usernameFrom } from '../../lib/password';
  import { session } from '../../lib/stores/session.svelte';
  import type { User } from '../../lib/api/types.gen';
  import { focusDialogResult } from './dialogFocus';
  import { settingsConfig } from './store.svelte';

  // Add person (§11.5): a name, a username and a role, then the details to
  // send them with a one-time password.
  let { open = $bindable(false), onadded }: { open?: boolean; onadded?: (u: User) => void } = $props();

  const formId = $props.id();
  const roles = [
    { value: 'member', label: 'Member' },
    { value: 'admin', label: 'Admin' },
  ];

  let name = $state('');
  let username = $state('');
  // The username follows the name until the admin types one.
  let ownUsername = false;
  let role = $state('member');
  let submitted = $state(false);
  let busy = $state(false);
  let taken = $state('');
  let nameRefused = $state('');
  let failed = $state('');
  let done = $state<{ name: string; username: string; password: string } | null>(null);

  const nameError = $derived(nameRefused || (submitted && !name.trim() ? 'Enter their name.' : undefined));
  const userError = $derived(taken === username && taken ? 'That username is taken. Try another.' : submitted ? usernameError(username) || undefined : undefined);
  const address = $derived(settingsConfig.value<string>('server.public_url', '') || location.origin);

  $effect(() => {
    if (open) return;
    name = username = taken = failed = nameRefused = '';
    ownUsername = false;
    role = 'member';
    submitted = false;
    done = null;
  });

  async function create(e: SubmitEvent) {
    e.preventDefault();
    submitted = true;
    failed = '';
    if (nameError || usernameError(username)) return;
    busy = true;
    try {
      // Installs from before team mode was on by default still run in single mode, which can't add people.
      if (session.me && session.me.mode !== 'multi') {
        await settingsConfig.save({ mode: 'multi' });
        await session.refresh().catch(() => {});
      }
      // No password in the request: the server makes a one-time password they replace at first sign-in (S9).
      const u = await api.post<UserCreateResp>('/api/users', { display_name: name.trim(), username: username.trim(), role });
      done = { name: u.display_name || name.trim(), username: u.username, password: u.one_time_password ?? '' };
      onadded?.(u);
      void focusDialogResult();
    } catch (err) {
      if (err instanceof ApiError && err.status === 409) taken = username;
      else if (/display name/.test((err as ApiError).message)) nameRefused = 'Use at most 64 characters, with no invisible ones.';
      else failed = refusal(err) || "Bothy couldn't add them. Try again.";
    } finally {
      busy = false;
    }
  }
</script>

<Dialog bind:open title="Add person" size="md" {busy} dirty={!done && !!(name || username)}>
  {#if done}
    <p class="text-body text-text">{done.name} can sign in now. Send them these details:</p>
    <CodeBlock class="mt-3" code={`Address: ${address}\nUsername: ${done.username}\nOne-time password: ${done.password}`} wrap />
    <p class="mt-3 text-body text-muted">They'll choose their own password when they first sign in.</p>
  {:else}
    <form id={formId} class="grid gap-5" onsubmit={create} novalidate>
      <TextField
        label="Name"
        bind:value={name}
        autocomplete="off"
        error={nameError}
        oninput={() => {
          nameRefused = '';
          if (!ownUsername) username = usernameFrom(name);
        }}
      />
      <TextField
        label="Username"
        bind:value={username}
        autocomplete="off"
        autocapitalize="none"
        spellcheck="false"
        help="They sign in with this."
        error={userError}
        oninput={() => (ownUsername = !!username)}
      />
      <div>
        <p class="field-label">Role</p>
        <Segmented class="mt-2" label="Role" options={roles} bind:value={role} />
        <p class="field-help">Admins can change settings and approve anything.</p>
      </div>
      {#if failed}<Notice tone="danger" text={failed} role="alert" />{/if}
    </form>
  {/if}
  {#snippet footer({ close })}
    {#if done}
      <Button variant="primary" onclick={close}>Done</Button>
    {:else}
      <Button data-cancel onclick={close} disabled={busy}>Cancel</Button>
      <Button variant="primary" type="submit" form={formId} loading={busy} loadingLabel="Creating…">Create</Button>
    {/if}
  {/snippet}
</Dialog>
