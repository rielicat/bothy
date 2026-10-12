<script lang="ts">
  import ConfigField from '../../components/ConfigField.svelte';
  import Dialog from '../../components/Dialog.svelte';
  import Section from '../../components/Section.svelte';
  import { api, errorText } from '../../lib/api/client';
  import type { User } from '../../lib/api/types.gen';
  import { config } from '../../lib/config.svelte';
  import { session } from '../../lib/stores/session.svelte';
  import { toasts } from '../../lib/stores/ui.svelte';

  let users = $state<User[]>([]);
  let form = $state({ username: '', display_name: '', password: '', role: 'member' });
  let error = $state('');
  let removing = $state<User | null>(null);
  let purge = $state(false);

  $effect(() => {
    api.get<User[]>('/api/users').then((u) => (users = u), (e) => (error = errorText(e)));
  });

  async function patch(u: User, body: Partial<User>) {
    try {
      Object.assign(u, await api.patch<User>(`/api/users/${encodeURIComponent(u.id)}`, body));
    } catch (e) {
      toasts.error(e);
    }
  }

  async function add(e: SubmitEvent) {
    e.preventDefault();
    error = '';
    try {
      users.push(await api.post<User>('/api/users', form));
      form = { username: '', display_name: '', password: '', role: 'member' };
      toasts.ok('Added. Share the password privately; they can change it under Settings → General.');
    } catch (err) {
      error = errorText(err);
    }
  }

  async function remove() {
    const u = removing!;
    try {
      await api.del(`/api/users/${encodeURIComponent(u.id)}${purge ? '?purge=true' : ''}`);
      u.disabled = true;
      if (purge) users = users.filter((x) => x.id !== u.id);
      toasts.ok(purge ? `${u.username} and their data were deleted.` : `${u.username} can no longer sign in.`);
    } catch (err) {
      toasts.error(err);
    } finally {
      removing = null;
      purge = false;
    }
  }
</script>

<Section title="People" desc="Everyone gets their own chats, memory, workspace and vault. Admins never see other people's messages.">
  {#if error}<p class="alert-bad my-3" role="alert">{error}</p>{/if}
  {#each users as u (u.id)}
    <div class="flex flex-wrap items-center gap-3 py-3.5 {u.disabled ? 'opacity-60' : ''}">
      <span class="grid size-9 place-items-center rounded-full bg-accent-soft font-semibold text-accent">{(u.display_name || u.username).slice(0, 1).toUpperCase()}</span>
      <div class="min-w-0 flex-1">
        <p class="font-medium">{u.display_name || u.username} {#if u.id === session.me?.user.id}<span class="chip">you</span>{/if}</p>
        <p class="text-xs text-muted">@{u.username}{u.disabled ? ' · disabled' : ''}</p>
      </div>
      {#if u.role === 'owner'}
        <span class="chip chip-accent">owner</span>
      {:else}
        <select class="input h-8 w-auto text-sm" aria-label="Role of {u.username}" value={u.role} onchange={(e) => patch(u, { role: e.currentTarget.value })}>
          <option value="member">Member</option>
          <option value="admin">Admin</option>
        </select>
        {#if u.disabled}
          <button type="button" class="btn btn-sm" onclick={() => patch(u, { disabled: false })}>Enable</button>
        {:else}
          <button type="button" class="btn btn-sm btn-ghost text-bad" onclick={() => (removing = u)}>Remove…</button>
        {/if}
      {/if}
    </div>
  {/each}
</Section>

<Section title="Daily spend caps" desc="Members get the default cap unless they have their own. Owners and admins are bounded by the global cap.">
  <ConfigField path="budgets.member_daily_usd" label="Default for members" type="usd" />
  {#each users.filter((u) => u.role === 'member') as u (u.id)}
    {#if config.entry(`budgets.per_user_daily_usd.${u.username}`)}
      <ConfigField path="budgets.per_user_daily_usd.{u.username}" label={u.display_name || u.username} type="usd" />
    {:else}
      <div class="flex items-center justify-between py-3 text-sm">
        <span>{u.display_name || u.username} <span class="text-muted">· default cap</span></span>
        <button type="button" class="btn btn-sm" onclick={() => config.set({ [`budgets.per_user_daily_usd.${u.username}`]: config.value('budgets.member_daily_usd', 2) })}>Set own cap</button>
      </div>
    {/if}
  {/each}
</Section>

<Section title="Add someone">
  <form class="grid gap-3 py-4 sm:grid-cols-2" onsubmit={add}>
    <div><label class="label" for="pu-user">Username</label><input id="pu-user" class="input" required autocapitalize="none" bind:value={form.username} /></div>
    <div><label class="label" for="pu-name">Display name</label><input id="pu-name" class="input" bind:value={form.display_name} /></div>
    <div><label class="label" for="pu-pass">Initial password</label><input id="pu-pass" class="input" type="password" autocomplete="new-password" minlength="12" required bind:value={form.password} /></div>
    <div>
      <label class="label" for="pu-role">Role</label>
      <select id="pu-role" class="input" bind:value={form.role}><option value="member">Member</option><option value="admin">Admin</option></select>
    </div>
    <button class="btn btn-primary justify-self-start">Add person</button>
  </form>
</Section>

<Dialog open={!!removing} title="Remove {removing?.display_name || removing?.username}?" onclose={() => (removing = null)}>
  <p class="text-sm text-muted">They are signed out everywhere and their API tokens stop working.</p>
  <label class="mt-4 flex items-start gap-2 text-sm">
    <input type="checkbox" class="mt-0.5 size-4 accent-bad" bind:checked={purge} />
    <span>Also delete everything they own: chats, memory, workspace files, vault secrets, grants and linked chat accounts. Audit entries are kept. This cannot be undone.</span>
  </label>
  {#snippet footer()}
    <button type="button" class="btn" onclick={() => (removing = null)}>Cancel</button>
    <button type="button" class="btn btn-danger" onclick={remove}>{purge ? 'Delete everything' : 'Remove'}</button>
  {/snippet}
</Dialog>
