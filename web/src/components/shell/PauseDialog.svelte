<script lang="ts">
  import { api } from '../../lib/api/client';
  import { session } from '../../lib/stores/session.svelte';
  import Icon from '../Icon.svelte';
  import Button from '../ui/Button.svelte';
  import Dialog from '../ui/Dialog.svelte';
  import { toast } from '../ui/toast.svelte';

  // docs/design/ui.md §11.6. Opened from the account menu and from Security.
  let { open = $bindable(false), onpaused }: { open?: boolean; onpaused?: () => void } = $props();

  let busy = $state(false);
  let error = $state('');

  $effect(() => {
    if (open) error = '';
  });

  async function pause() {
    busy = true;
    error = '';
    try {
      await api.post('/api/admin/pause', { paused: true });
      session.paused = true;
      busy = false;
      open = false;
      toast('Bothy paused.');
      onpaused?.();
    } catch {
      error = "Couldn't pause Bothy. Try again.";
    } finally {
      busy = false;
    }
  }
</script>

<Dialog bind:open title="Pause Bothy?" initialFocus="cancel" {busy}>
  <p class="text-body text-text">
    Everything running stops now, and nothing new starts, including schedules, until someone resumes it. Approvals that are waiting will be cancelled.
  </p>
  {#if error}<p class="field-error" role="alert"><Icon name="alert" class="mt-px" />{error}</p>{/if}
  {#snippet footer({ close })}
    <Button data-cancel onclick={close}>Cancel</Button>
    <Button variant="danger" loading={busy} loadingLabel="Pausing…" onclick={pause}>Pause Bothy</Button>
  {/snippet}
</Dialog>
