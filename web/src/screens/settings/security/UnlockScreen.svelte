<script lang="ts">
  import { untrack } from 'svelte';
  import { Button, Page } from '../../../components/ui';
  import { api } from '../../../lib/api/client';
  import type { VaultStatus } from '../../../lib/api/types.gen';
  import PassphraseFields from './PassphraseFields.svelte';
  import { CHOOSE } from './vault';

  // The blocking screen admins see while saved keys are locked (§13.2); the
  // shell mounts it in place of the page, beside the sidebar, and drops it on onunlocked.
  let { onunlocked }: { onunlocked?: () => void } = $props();

  let busy = $state(false);
  let fields: PassphraseFields | undefined = $state();
  let status = $state<VaultStatus | null>(null);
  $effect(() => untrack(() => void api.get<VaultStatus>('/api/vault/status').then((s) => (status = s), () => {})));
  // Locked by a key file, a passphrase can't help; a first passphrase is chosen, so it is typed twice.
  const keyFile = $derived(!!status?.kek_source && status.kek_source !== 'passphrase');
  const first = $derived(!!status?.new_passphrase);
</script>

<Page title={first ? 'Choose a passphrase' : 'Unlock saved keys'}>
  {#if keyFile}
    <p class="text-reading text-muted">
      Bothy can't reach its models or connections: its saved keys are locked because the key file it was set up with is missing or doesn't match, so a passphrase can't unlock them. Put that key
      file back on the server, then restart Bothy.
    </p>
  {:else}
    <p class="text-reading text-muted">
      {first ? CHOOSE : "Bothy can't reach its models or connections until you enter the passphrase."}
    </p>
    <form class="mt-6 max-w-[30rem]" onsubmit={(e) => fields?.submit(e)} novalidate>
      <PassphraseFields bind:this={fields} {first} bind:busy onunlocked={() => onunlocked?.()} />
      <Button variant="primary" type="submit" class="mt-4 max-sm:w-full" loading={busy} loadingLabel={first ? 'Saving…' : 'Unlocking…'}>{first ? 'Save passphrase' : 'Unlock'}</Button>
    </form>
  {/if}
</Page>
