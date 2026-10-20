<script lang="ts">
  import { Button, Dialog, toast } from '../../../components/ui';
  import PassphraseFields from './PassphraseFields.svelte';
  import { CHOOSE } from './vault';

  // first: no passphrase chosen yet (VaultStatus.new_passphrase), which this sets, so it is typed twice.
  let { open = $bindable(false), onunlocked, first = false }: { open?: boolean; onunlocked?: () => void; first?: boolean } = $props();

  const uid = $props.id();
  let passphrase = $state('');
  let busy = $state(false);
  let fields: PassphraseFields | undefined = $state();
</script>

<!-- The fields go with the closed dialog's body, so only the passphrase that marks it dirty needs clearing. -->
<Dialog bind:open title={first ? 'Choose a passphrase' : 'Unlock saved keys'} {busy} dirty={!!passphrase} onclose={() => (passphrase = '')} cancel>
  <form id="{uid}-form" onsubmit={(e) => fields?.submit(e)} novalidate>
    <p class="text-body text-text">{first ? CHOOSE : "Enter the passphrase to unlock Bothy's saved keys."}</p>
    <PassphraseFields
      bind:this={fields}
      class="mt-4"
      {first}
      bind:passphrase
      bind:busy
      onunlocked={() => {
        open = false;
        toast('Saved keys unlocked.');
        onunlocked?.();
      }}
    />
  </form>
  {#snippet footer()}
    <Button variant="primary" type="submit" form="{uid}-form" loading={busy} loadingLabel={first ? 'Saving…' : 'Unlocking…'}>{first ? 'Save passphrase' : 'Unlock'}</Button>
  {/snippet}
</Dialog>
