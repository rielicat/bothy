<script lang="ts">
  import { untrack } from 'svelte';
  import { Avatar, Button, Checkbox, CodeBlock, Dialog, EmptyState, IconButton, Menu, Notice, StatusLine, TextField, toast, type MenuItem } from '../../components/ui';
  import { api, ApiError } from '../../lib/api/client';
  import { router } from '../../lib/router.svelte';
  import { session } from '../../lib/stores/session.svelte';
  import AddPerson from './AddPerson.svelte';
  import ConfirmDialog from './ConfirmDialog.svelte';
  import { focusDialogResult } from './dialogFocus';
  import type { User } from '../../lib/api/types.gen';
  import { displayName, moneyText, parseMoney, roleLine } from './accountHelpers';
  import SettingsFrame from './SettingsFrame.svelte';
  import { settingsConfig } from './store.svelte';

  let users = $state<User[]>([]);
  let loaded = $state(false);
  let failed = $state(false);
  let actionError = $state('');
  let addOpen = $state(false);

  async function load() {
    failed = false;
    try {
      users = await api.get<User[]>('/api/users');
      loaded = true;
    } catch {
      failed = true;
    }
  }

  $effect(() => {
    untrack(() => {
      void load();
      // Setup's "Add your team" lands here with the dialog open (§5.7).
      if (router.query.get('add') === '1') {
        addOpen = true;
        router.setQuery({ add: null });
      }
    });
  });

  const order: Record<string, number> = { owner: 0, admin: 1, member: 2 };
  const sorted = $derived([...users].sort((a, b) => (order[a.role] ?? 3) - (order[b.role] ?? 3) || displayName(a).localeCompare(displayName(b))));
  const solo = $derived(loaded && users.length <= 1);
  const meId = $derived(session.me?.user.id);

  async function patch(u: User, body: Partial<User>, ok: string, fail: string) {
    actionError = '';
    try {
      Object.assign(u, await api.patch<User>(`/api/users/${encodeURIComponent(u.id)}`, body));
      toast(ok);
    } catch {
      actionError = fail;
    }
  }

  function items(u: User): MenuItem[] {
    const name = displayName(u);
    return [
      { label: 'Member', group: 'Change role', checked: u.role === 'member', onselect: () => u.role !== 'member' && patch(u, { role: 'member' }, 'Role changed.', `Couldn't change ${name}'s role. Try again.`) },
      { label: 'Admin', group: 'Change role', checked: u.role === 'admin', onselect: () => u.role !== 'admin' && patch(u, { role: 'admin' }, 'Role changed.', `Couldn't change ${name}'s role. Try again.`) },
      'separator',
      { label: 'Spending limit…', onselect: () => openLimit(u) },
      { label: 'Reset password…', onselect: () => openReset(u) },
      u.disabled
        ? { label: 'Turn on account', onselect: () => patch(u, { disabled: false }, 'Account turned on.', `Couldn't turn on ${name}'s account. Try again.`) }
        : { label: 'Turn off account…', danger: true, onselect: () => (turningOff = u) },
    ];
  }

  // Spending limit: budgets.per_user_daily_usd is keyed by username.
  let limitFor = $state<User | null>(null);
  let limitText = $state('');
  let noLimit = $state(true);
  let limitBusy = $state(false);
  let limitError = $state('');
  let limitSubmitted = $state(false);
  const memberDefault = $derived(settingsConfig.value<number>('budgets.member_daily_usd', 0));

  function personalLimit(username: string): number | null {
    const own = settingsConfig.value<number | null>(`budgets.per_user_daily_usd.${username}`, null);
    if (typeof own === 'number') return own;
    const map = settingsConfig.value<Record<string, number>>('budgets.per_user_daily_usd', {});
    return typeof map?.[username] === 'number' ? map[username] : null;
  }

  function openLimit(u: User) {
    const v = personalLimit(u.username);
    limitFor = u;
    noLimit = v === null;
    limitText = v === null ? '' : moneyText(v);
    limitError = '';
    limitSubmitted = false;
  }

  const limitFieldError = $derived(limitSubmitted && !noLimit && parseMoney(limitText) === null ? 'Enter an amount in dollars, like 5.00.' : undefined);

  async function saveLimit(e: SubmitEvent) {
    e.preventDefault();
    const u = limitFor;
    if (!u) return;
    limitSubmitted = true;
    if (limitFieldError) return;
    limitBusy = true;
    limitError = '';
    try {
      await settingsConfig.save({ [`budgets.per_user_daily_usd.${u.username}`]: noLimit ? null : parseMoney(limitText) });
      limitBusy = false;
      limitFor = null;
      toast('Limit saved.');
    } catch {
      limitBusy = false;
      limitError = "Couldn't save. Try again.";
    }
  }

  // Reset password (S9): the server makes a one-time password.
  let resetFor = $state<User | null>(null);
  let resetBusy = $state(false);
  let resetError = $state('');
  let resetPassword = $state('');

  function openReset(u: User) {
    resetFor = u;
    resetError = resetPassword = '';
  }

  async function reset() {
    const u = resetFor;
    if (!u) return;
    resetBusy = true;
    resetError = '';
    try {
      const r = await api.post<{ one_time_password: string }>(`/api/users/${encodeURIComponent(u.id)}/password`);
      resetPassword = r.one_time_password;
      void focusDialogResult();
    } catch (err) {
      resetError =
        err instanceof ApiError && (err.status === 404 || err.status === 405 || err.status === 501)
          ? "This version of Bothy can't reset passwords yet. Update Bothy, then try again."
          : "Bothy couldn't reset the password. Try again.";
    } finally {
      resetBusy = false;
    }
  }

  let turningOff = $state<User | null>(null);
  let offBusy = $state(false);
  let offError = $state('');

  async function turnOff() {
    const u = turningOff;
    if (!u) return;
    offBusy = true;
    offError = '';
    try {
      await api.del(`/api/users/${encodeURIComponent(u.id)}`);
      u.disabled = true;
      offBusy = false;
      turningOff = null;
      toast('Account turned off.');
    } catch {
      offBusy = false;
      offError = `Bothy couldn't turn off ${displayName(u)}'s account. Try again.`;
    }
  }

  const address = $derived(settingsConfig.value<string>('server.public_url', '') || location.origin);
</script>

<SettingsFrame title="People" loading={!loaded && !failed}>
  {#snippet actions()}
    {#if loaded && !solo}<Button onclick={() => (addOpen = true)}>Add person</Button>{/if}
  {/snippet}

  {#if failed}
    <StatusLine tone="danger" text="This page didn't load. Check your connection and try again." action={{ label: 'Try again', onclick: load }} />
  {:else if solo}
    <EmptyState text="It's just you so far. Add teammates so they get their own sign-in." action={{ label: 'Add person', onclick: () => (addOpen = true) }} />
  {:else if loaded}
    {#if actionError}<Notice class="mb-4" tone="danger" text={actionError} role="alert" />{/if}
    <ul>
      {#each sorted as u (u.id)}
        {@const name = displayName(u)}
        <li class="flex min-h-14 items-center gap-3 border-b border-border py-2 last:border-b-0">
          <Avatar {name} size={32} />
          <div class="min-w-0 flex-1 sm:grid sm:grid-cols-[minmax(0,1fr)_minmax(0,9rem)_minmax(0,14rem)] sm:items-center sm:gap-4">
            <p class="truncate text-body font-medium text-text" title={name}>{name}</p>
            <p class="truncate text-small text-muted max-sm:hidden">{u.username}</p>
            <p class="truncate text-small text-muted max-sm:hidden">{roleLine(u, u.id === session.me?.user.id)}</p>
            <p class="truncate text-small text-muted sm:hidden">{u.username} · {roleLine(u, u.id === session.me?.user.id)}</p>
          </div>
          <!-- The owner can't be changed, and nobody changes their own role or account here. -->
          <div class="flex size-8 shrink-0 items-center justify-center pointer-coarse:size-11">
            {#if u.role !== 'owner' && u.id !== meId}
              <Menu items={items(u)} placement="bottom-end">
                {#snippet trigger(props)}<IconButton icon="more" label="Options for {name}" size="sm" variant="quiet" {...props} />{/snippet}
              </Menu>
            {/if}
          </div>
        </li>
      {/each}
    </ul>
  {/if}
</SettingsFrame>

<AddPerson bind:open={addOpen} onadded={(u) => (users = [...users, u])} />

<Dialog open={!!limitFor} title="Spending limit" size="md" busy={limitBusy} onclose={() => (limitFor = null)}>
  {#if limitFor}
    {@const u = limitFor}
    <form id="limit-form" class="grid gap-3" onsubmit={saveLimit} novalidate>
      <TextField label="Daily limit for {displayName(u)}" prefix="$" inputmode="decimal" bind:value={limitText} disabled={noLimit} error={limitFieldError} />
      <Checkbox
        label="No personal limit"
        description={u.role === 'member' && memberDefault > 0 ? `Members without their own limit can spend up to $${moneyText(memberDefault)} a day.` : undefined}
        bind:checked={noLimit}
      />
      {#if limitError}<Notice tone="danger" text={limitError} role="alert" />{/if}
    </form>
  {/if}
  {#snippet footer({ close })}
    <Button data-cancel onclick={close} disabled={limitBusy}>Cancel</Button>
    <Button variant="primary" type="submit" form="limit-form" loading={limitBusy} loadingLabel="Saving…">Save</Button>
  {/snippet}
</Dialog>

<Dialog
  open={!!resetFor}
  title={resetFor ? (resetPassword ? `New password for ${displayName(resetFor)}` : `Reset ${displayName(resetFor)}'s password?`) : ''}
  busy={resetBusy}
  initialFocus="cancel"
  onclose={() => (resetFor = null)}
>
  {#if resetFor && resetPassword}
    <p class="text-body text-text">Send {displayName(resetFor)} these details:</p>
    <CodeBlock class="mt-3" code={`Address: ${address}\nUsername: ${resetFor.username}\nOne-time password: ${resetPassword}`} wrap />
    <p class="mt-3 text-body text-muted">They'll choose their own password when they sign in.</p>
  {:else}
    <p class="text-body text-text">Bothy makes a one-time password for them to sign in with. Their current password stops working.</p>
    {#if resetError}<Notice class="mt-3" tone="danger" text={resetError} role="alert" />{/if}
  {/if}
  {#snippet footer({ close })}
    {#if resetPassword}
      <Button variant="primary" onclick={close}>Done</Button>
    {:else}
      <Button data-cancel onclick={close} disabled={resetBusy}>Cancel</Button>
      <Button variant="primary" loading={resetBusy} loadingLabel="Resetting…" onclick={reset}>Reset password</Button>
    {/if}
  {/snippet}
</Dialog>

<ConfirmDialog
  open={!!turningOff}
  title="Turn off {turningOff ? displayName(turningOff) : ''}'s account?"
  confirm="Turn off account"
  busyLabel="Turning off…"
  busy={offBusy}
  error={offError}
  onconfirm={turnOff}
  onclose={() => {
    turningOff = null;
    offError = '';
  }}>They're signed out everywhere and can't sign in until an admin turns the account back on. Their chats and memory are kept.</ConfirmDialog
>
