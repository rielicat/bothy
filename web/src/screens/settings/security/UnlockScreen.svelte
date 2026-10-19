<script lang="ts">
  import { untrack } from 'svelte';
  import { Button, CodeBlock, Disclosure, Page, TextField } from '../../../components/ui';
  import { api } from '../../../lib/api/client';
  import type { VaultStatus } from '../../../lib/api/types.gen';
  import { unlock } from './vault';

  // The blocking screen admins see while saved keys are locked (§13.2); the
  // shell mounts it in place of the page, beside the sidebar, and drops it on onunlocked.
  let { onunlocked }: { onunlocked?: () => void } = $props();

  let passphrase = $state('');
  let again = $state('');
  let error = $state('');
  let details = $state('');
  let busy = $state(false);
  let input: HTMLInputElement | undefined = $state();
  let status = $state<VaultStatus | null>(null);
  $effect(() => untrack(() => void api.get<VaultStatus>('/api/vault/status').then((s) => (status = s), () => {})));
  // Locked by a key file, a passphrase can't help; a first passphrase is chosen, so it is typed twice.
  const keyFile = $derived(!!status?.kek_source && status.kek_source !== 'passphrase');
  const first = $derived(!!status?.new_passphrase);

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    if (first && [...passphrase].length < 12) error = 'Use at least 12 characters.';
    else if (first && again !== passphrase) error = "The two passphrases don't match.";
    else {
      busy = true;
      const r = await unlock(passphrase);
      busy = false;
      if (r.ok) return onunlocked?.();
      error = r.error;
      details = r.details ?? '';
    }
    input?.focus();
  }
</script>

<Page title={first ? 'Choose a passphrase' : 'Unlock saved keys'}>
  {#if keyFile}
    <p class="text-reading text-muted">
      Bothy can't reach its models or connections: its saved keys are locked because the key file it was set up with is missing or doesn't match, so a passphrase can't unlock them. Put that key
      file back on the server, then restart Bothy.
    </p>
  {:else}
    <p class="text-reading text-muted">
      {first
        ? 'Bothy keeps its saved keys locked with a passphrase. Choose one of at least 12 characters and keep it safe: without it, nobody can unlock them.'
        : "Bothy can't reach its models or connections until you enter the passphrase."}
    </p>
    <form class="mt-6 max-w-[30rem]" onsubmit={submit} novalidate>
      <TextField label="Passphrase" type="password" autocomplete={first ? 'new-password' : 'off'} bind:value={passphrase} bind:input error={first ? undefined : error} />
      {#if first}<TextField class="mt-4" label="Type it again" type="password" autocomplete="new-password" bind:value={again} {error} />{/if}
      {#if details}
        <Disclosure class="mt-2"><CodeBlock code={details} wrap /></Disclosure>
      {/if}
      <Button variant="primary" type="submit" class="mt-4 max-sm:w-full" loading={busy} loadingLabel={first ? 'Saving…' : 'Unlocking…'}>{first ? 'Save passphrase' : 'Unlock'}</Button>
    </form>
  {/if}
</Page>
