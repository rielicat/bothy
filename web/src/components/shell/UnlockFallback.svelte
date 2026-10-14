<script lang="ts">
  import { api, ApiError } from '../../lib/api/client';
  import Button from '../ui/Button.svelte';
  import Page from '../ui/Page.svelte';
  import TextField from '../ui/TextField.svelte';

  // The sealed-vault screen (docs/design/ui.md §13.2) until C9's UnlockScreen
  // is in the tree; Shell prefers that one when it exists.
  let { onunlocked }: { onunlocked?: () => void } = $props();

  let passphrase = $state('');
  let busy = $state(false);
  let error = $state('');

  async function unlock(e: SubmitEvent) {
    e.preventDefault();
    if (!passphrase) {
      error = 'Enter the passphrase.';
      return;
    }
    busy = true;
    error = '';
    try {
      await api.post('/api/vault/unseal', { passphrase });
      passphrase = '';
      onunlocked?.();
    } catch (err) {
      error = err instanceof ApiError && err.status < 500 ? "That passphrase didn't unlock the keys. Check it and try again." : "Bothy isn't responding. It may be restarting.";
    } finally {
      busy = false;
    }
  }
</script>

<Page title="Unlock Bothy">
  <p class="text-reading text-text">Bothy's saved keys are locked. Enter the passphrase to unlock them.</p>
  <form class="mt-6 max-w-[30rem]" onsubmit={unlock}>
    <TextField label="Passphrase" type="password" bind:value={passphrase} {error} autocomplete="current-password" />
    <Button class="mt-4" variant="primary" type="submit" loading={busy} loadingLabel="Unlocking…">Unlock</Button>
  </form>
</Page>
