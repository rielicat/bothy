<script lang="ts">
  import { Button, CodeBlock, Dialog, Disclosure, TextField, toast } from '../../../components/ui';
  import { CHOOSE, unlock } from './vault';

  // first: no passphrase chosen yet (VaultStatus.new_passphrase), which this sets, so it is typed twice.
  let { open = $bindable(false), onunlocked, first = false }: { open?: boolean; onunlocked?: () => void; first?: boolean } = $props();

  const uid = $props.id();
  let passphrase = $state('');
  let again = $state('');
  let error = $state('');
  let details = $state('');
  let busy = $state(false);
  let input: HTMLInputElement | undefined = $state();

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    busy = true;
    const r = await unlock(passphrase, first ? again : undefined);
    busy = false;
    if (r.ok) {
      open = false;
      toast('Saved keys unlocked.');
      onunlocked?.();
      return;
    }
    error = r.error;
    details = r.details ?? '';
    input?.focus();
  }

  function reset() {
    passphrase = again = error = details = '';
  }
</script>

<Dialog bind:open title={first ? 'Choose a passphrase' : 'Unlock saved keys'} {busy} dirty={!!passphrase} onclose={reset}>
  <form id="{uid}-form" onsubmit={submit} novalidate>
    <p class="text-body text-text">{first ? CHOOSE : "Enter the passphrase to unlock Bothy's saved keys."}</p>
    <TextField class="mt-4" label="Passphrase" type="password" autocomplete={first ? 'new-password' : 'off'} bind:value={passphrase} bind:input error={first ? undefined : error} />
    {#if first}<TextField class="mt-4" label="Type it again" type="password" autocomplete="new-password" bind:value={again} {error} />{/if}
    {#if details}
      <Disclosure class="mt-2"><CodeBlock code={details} wrap /></Disclosure>
    {/if}
  </form>
  {#snippet footer({ close })}
    <Button data-cancel onclick={close}>Cancel</Button>
    <Button variant="primary" type="submit" form="{uid}-form" loading={busy} loadingLabel={first ? 'Saving…' : 'Unlocking…'}>{first ? 'Save passphrase' : 'Unlock'}</Button>
  {/snippet}
</Dialog>
