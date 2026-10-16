<script lang="ts">
  import { Button, Dialog, toast } from '../../components/ui';
  import { api } from '../../lib/api/client';
  type Named = { id: string; name: string };

  let {
    schedule = $bindable(null),
    team = false,
    ondeleted,
  }: {
    schedule: Named | null;
    /** A schedule someone set up in a chat app, whose results went to that chat. */
    team?: boolean;
    ondeleted: (s: Named) => void;
  } = $props();

  let busy = $state(false);
  let error = $state('');
  let open = $state(false);
  let name = $state('');
  $effect(() => {
    if (schedule) name = schedule.name;
    open = !!schedule;
  });

  async function remove() {
    const s = schedule;
    if (!s) return;
    busy = true;
    error = '';
    try {
      await api.del(`/api/schedules/${encodeURIComponent(s.id)}`);
      schedule = null;
      toast('Schedule deleted.');
      ondeleted(s);
    } catch {
      error = "Couldn't delete it. Try again.";
    } finally {
      busy = false;
    }
  }
</script>

<Dialog
  bind:open
  title={team ? `Delete “${name}”?` : `Delete the schedule “${name}”?`}
  initialFocus="cancel"
  {busy}
  onclose={() => {
    schedule = null;
    error = '';
  }}
>
  <p class="text-body text-text">{team ? "It stops running. Past results stay in that chat. This can't be undone." : "This can't be undone."}</p>
  {#if error}<p class="field-error mt-2" role="alert">{error}</p>{/if}
  {#snippet footer({ close })}
    <Button data-cancel onclick={close}>Cancel</Button>
    <Button variant="danger" loading={busy} loadingLabel="Deleting…" onclick={remove}>Delete schedule</Button>
  {/snippet}
</Dialog>
