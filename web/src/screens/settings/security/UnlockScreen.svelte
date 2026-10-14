<script lang="ts">
  import { Button, CodeBlock, Disclosure, TextField } from '../../../components/ui';
  import { unlock } from './vault';

  // The blocking screen admins see while saved keys are locked (§13.2); the
  // shell mounts it in place of the app and drops it on onunlocked.
  let { onunlocked }: { onunlocked?: () => void } = $props();

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
      onunlocked?.();
      return;
    }
    error = r.error;
    details = r.details ?? '';
    input?.focus();
  }
</script>

<svelte:head><title>Unlock Bothy · Bothy</title></svelte:head>

<div class="mx-auto w-full max-w-[30rem] px-6 pt-[15vh] pb-16 max-sm:px-4 max-sm:pt-6">
  <p class="text-heading text-text">Bothy</p>
  <h1 class="mt-10 text-display text-text" tabindex="-1">Unlock Bothy</h1>
  <p class="mt-2 text-reading text-muted">Bothy's saved keys are locked. Enter the passphrase to unlock them.</p>
  <form class="mt-8" onsubmit={submit} novalidate>
    <TextField label="Passphrase" type="password" autocomplete="off" bind:value={passphrase} bind:input {error} />
    {#if details}
      <Disclosure class="mt-2"><CodeBlock code={details} wrap /></Disclosure>
    {/if}
    <Button variant="primary" type="submit" class="mt-8 h-10 max-sm:w-full" loading={busy} loadingLabel="Unlocking…">Unlock</Button>
  </form>
</div>
