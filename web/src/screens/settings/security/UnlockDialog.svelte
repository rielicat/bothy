<script lang="ts">
  import { Button, CodeBlock, Dialog, Disclosure, TextField, toast } from '../../../components/ui';
  import { unlock } from './vault';

  let { open = $bindable(false), onunlocked }: { open?: boolean; onunlocked?: () => void } = $props();

  const uid = $props.id();
  let passphrase = $state('');
  let error = $state('');
  let details = $state('');
  let busy = $state(false);
  let input: HTMLInputElement | undefined = $state();

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    busy = true;
    const r = await unlock(passphrase);
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
    passphrase = '';
    error = '';
    details = '';
  }
</script>

<Dialog bind:open title="Unlock saved keys" {busy} dirty={!!passphrase} onclose={reset}>
  <form id="{uid}-form" onsubmit={submit} novalidate>
    <p class="text-body text-text">Enter the passphrase to unlock Bothy's saved keys.</p>
    <TextField class="mt-4" label="Passphrase" type="password" autocomplete="off" bind:value={passphrase} bind:input {error} />
    {#if details}
      <Disclosure class="mt-2"><CodeBlock code={details} wrap /></Disclosure>
    {/if}
  </form>
  {#snippet footer({ close })}
    <Button data-cancel onclick={close}>Cancel</Button>
    <Button variant="primary" type="submit" form="{uid}-form" loading={busy} loadingLabel="Unlocking…">Unlock</Button>
  {/snippet}
</Dialog>
