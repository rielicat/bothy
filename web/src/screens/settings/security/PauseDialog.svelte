<script lang="ts">
  import { Button, Dialog, toast } from '../../../components/ui';
  import { api } from '../../../lib/api/client';
  import { session } from '../../../lib/stores/session.svelte';

  // §11.6. C1 builds the same dialog for the account menu in
  // components/shell/; C10 keeps one of the two.
  let { open = $bindable(false), onpaused }: { open?: boolean; onpaused?: () => void } = $props();

  let busy = $state(false);
  let error = $state('');

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
      busy = false;
      error = "Couldn't pause Bothy. Try again.";
    }
  }
</script>

<Dialog bind:open title="Pause Bothy?" initialFocus="cancel" {busy} onclose={() => (error = '')}>
  <p class="text-body text-text">
    Everything running stops now, and nothing new starts, including schedules, until someone resumes it. Approvals that are waiting will be cancelled.
  </p>
  {#if error}<p class="field-error" role="alert">{error}</p>{/if}
  {#snippet footer({ close })}
    <Button data-cancel onclick={close}>Cancel</Button>
    <Button variant="danger" loading={busy} loadingLabel="Pausing…" onclick={pause}>Pause Bothy</Button>
  {/snippet}
</Dialog>
