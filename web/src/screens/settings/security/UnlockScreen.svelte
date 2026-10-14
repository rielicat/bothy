<script lang="ts">
  import { Button, CodeBlock, Disclosure, Page, TextField } from '../../../components/ui';
  import { unlock } from './vault';

  // The blocking screen admins see while saved keys are locked (§13.2); the
  // shell mounts it in place of the page, beside the sidebar, and drops it on onunlocked.
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

<Page title="Unlock Bothy">
  <p class="text-reading text-muted">Bothy's saved keys are locked. Enter the passphrase to unlock them.</p>
  <form class="mt-6 max-w-[30rem]" onsubmit={submit} novalidate>
    <TextField label="Passphrase" type="password" autocomplete="off" bind:value={passphrase} bind:input {error} />
    {#if details}
      <Disclosure class="mt-2"><CodeBlock code={details} wrap /></Disclosure>
    {/if}
    <Button variant="primary" type="submit" class="mt-4 max-sm:w-full" loading={busy} loadingLabel="Unlocking…">Unlock</Button>
  </form>
</Page>
