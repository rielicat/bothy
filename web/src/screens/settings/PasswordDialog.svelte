<script lang="ts">
  import { untrack } from 'svelte';
  import { Button, Dialog, LinkButton, Notice, TextField, toast } from '../../components/ui';
  import { api, ApiError } from '../../lib/api/client';
  import { passwordError, passwordHelp } from '../../lib/password';
  import { session } from '../../lib/stores/session.svelte';
  import { loginError } from '../setup/auth';

  let { open = $bindable(false) }: { open?: boolean } = $props();

  const formId = $props.id();
  let current = $state('');
  let next = $state('');
  let show = $state(false);
  let busy = $state(false);
  let submitted = $state(false);
  let serverError = $state<{ current?: string; next?: string; form?: string }>({});

  // The help is a live region, so it changes at most every 500 ms (§5.5).
  let helpText = $state(passwordHelp(''));
  let lastHelp = 0;
  $effect(() => {
    const text = passwordHelp(next);
    const wait = untrack(() => Math.max(0, lastHelp + 500 - Date.now()));
    const t = setTimeout(() => {
      helpText = text;
      lastHelp = Date.now();
    }, wait);
    return () => clearTimeout(t);
  });

  const currentError = $derived(serverError.current ?? (submitted && !current ? 'Enter your current password.' : undefined));
  const nextError = $derived(serverError.next ?? (submitted ? passwordError(next) || undefined : undefined));

  $effect(() => {
    if (open) return;
    current = next = '';
    show = submitted = false;
    serverError = {};
  });

  async function save(e: SubmitEvent) {
    e.preventDefault();
    submitted = true;
    serverError = {};
    if (currentError || nextError) return;
    busy = true;
    try {
      await api.post('/api/auth/password', { old: current, new: next });
      // The server signed every session out and gave this one a new cookie and CSRF token.
      await session.refresh().catch(() => {});
      busy = false;
      open = false;
      toast('Password changed.');
    } catch (err) {
      busy = false;
      const m = err instanceof ApiError ? err.message : '';
      if (err instanceof ApiError && err.status === 429) serverError = { form: loginError(err) };
      else if (err instanceof ApiError && (err.status === 401 || err.status === 403 || /current password/.test(m))) serverError = { current: "That's not your current password." };
      else if (/differ/.test(m)) serverError = { next: 'Choose a password different from your current one.' };
      else if (/common/i.test(m)) serverError = { next: 'This password is too common. Try a phrase of a few words.' };
      else if (err instanceof ApiError && err.status === 400) serverError = { next: 'Use at least 12 characters.' };
      else serverError = { form: "Bothy didn't respond. Check your connection, then try again." };
    }
  }
</script>

<Dialog bind:open title="Change password" size="md" {busy} dirty={!!(current || next)} cancel lockCancel>
  <form id={formId} class="grid gap-5" onsubmit={save} novalidate>
    <TextField label="Current password" type="password" autocomplete="current-password" bind:value={current} error={currentError} />
    <TextField id="{formId}-new" label="New password" type={show ? 'text' : 'password'} autocomplete="new-password" bind:value={next} error={nextError}>
      {#snippet labelAction()}<LinkButton onclick={() => (show = !show)} aria-pressed={show} aria-controls="{formId}-new">{show ? 'Hide' : 'Show'}</LinkButton>{/snippet}
      {#snippet help()}<span aria-live="polite">{helpText}</span>{/snippet}
    </TextField>
    {#if serverError.form}<Notice tone="danger" text={serverError.form} role="alert" />{/if}
    <!-- For password managers, which file the new password under this username. -->
    <input type="text" class="hidden" autocomplete="username" value={session.me?.user.username ?? ''} readonly tabindex="-1" aria-hidden="true" />
  </form>
  {#snippet footer()}
    <Button variant="primary" type="submit" form={formId} loading={busy} loadingLabel="Saving…">Save password</Button>
  {/snippet}
</Dialog>
