<script lang="ts">
  import { Button, CodeBlock, Dialog, Notice, Segmented, TextField } from '../../components/ui';
  import { api, ApiError } from '../../lib/api/client';
  import { session } from '../../lib/stores/session.svelte';
  import { generatePassword, usernameError, type UserCreateResp, type UserV2 } from './accountHelpers';
  import { focusDialogResult } from './dialogFocus';
  import { settingsConfig } from './store.svelte';

  // Add person (§11.5): a name, a username and a role, then the details to
  // send them with a one-time password.
  let { open = $bindable(false), onadded }: { open?: boolean; onadded?: (u: UserV2) => void } = $props();

  const formId = $props.id();
  const roles = [
    { value: 'member', label: 'Member' },
    { value: 'admin', label: 'Admin' },
  ];

  let name = $state('');
  let username = $state('');
  let role = $state('member');
  let submitted = $state(false);
  let busy = $state(false);
  let taken = $state('');
  let failed = $state('');
  let done = $state<{ name: string; username: string; password: string; mustChange: boolean } | null>(null);

  const nameError = $derived(submitted && !name.trim() ? 'Enter their name.' : undefined);
  const userError = $derived(taken === username && taken ? 'That username is taken. Try another.' : submitted ? usernameError(username) || undefined : undefined);
  const address = $derived(settingsConfig.value<string>('server.public_url', '') || location.origin);

  $effect(() => {
    if (open) return;
    name = username = taken = failed = '';
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
    const password = generatePassword();
    try {
      // Installs from before team mode was on by default still run in single mode, which can't add people.
      if (session.me && session.me.mode !== 'multi') {
        await settingsConfig.save({ mode: 'multi' });
        await session.refresh().catch(() => {});
      }
      const u = await api.post<UserCreateResp>('/api/users', { display_name: name.trim(), username, role, password });
      done = { name: u.display_name || name.trim(), username: u.username, password: u.one_time_password || password, mustChange: !!u.must_change_password };
      onadded?.(u);
      void focusDialogResult();
    } catch (err) {
      if (err instanceof ApiError && err.status === 409) taken = username;
      else failed = "Bothy couldn't add them. Try again.";
    } finally {
      busy = false;
    }
  }
</script>

<Dialog bind:open title="Add person" size="md" {busy} dirty={!done && !!(name || username)}>
  {#if done}
    <p class="text-body text-text">{done.name} can sign in now. Send them these details:</p>
    <CodeBlock class="mt-3" code={`Address: ${address}\nUsername: ${done.username}\n${done.mustChange ? 'One-time password' : 'Password'}: ${done.password}`} wrap />
    <p class="mt-3 text-body text-muted">
      {done.mustChange ? "They'll choose their own password when they first sign in." : 'They can choose their own password in Settings → Account after they sign in.'}
    </p>
  {:else}
    <form id={formId} class="grid gap-5" onsubmit={create} novalidate>
      <TextField label="Name" bind:value={name} autocomplete="off" error={nameError} />
      <TextField label="Username" bind:value={username} autocomplete="off" autocapitalize="none" spellcheck="false" help="They sign in with this." error={userError} />
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
