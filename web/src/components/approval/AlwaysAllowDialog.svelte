<script lang="ts">
  import Button from '../ui/Button.svelte';
  import Dialog from '../ui/Dialog.svelte';
  import Verbatim from '../Verbatim.svelte';

  // Confirms a standing permission (§8.3). It has no keyboard shortcut, so a
  // stray key press can never create one.
  let {
    open = $bindable(false),
    grant,
    busy = false,
    onconfirm,
  }: {
    open?: boolean;
    /** "sending messages to this Slack chat" */
    grant: string;
    busy?: boolean;
    onconfirm: () => void;
  } = $props();
</script>

<Dialog bind:open title="Always allow this?" {busy}>
  <p class="text-body text-text">
    Bothy won't ask again before <span data-copy-ignore>“<Verbatim text={grant} />”</span>, in any chat. You can remove this in Settings → Security.
  </p>
  {#snippet footer({ close })}
    <Button data-cancel disabled={busy} onclick={close}>Cancel</Button>
    <Button variant="primary" loading={busy} loadingLabel="Allowing…" onclick={onconfirm}>Always allow</Button>
  {/snippet}
</Dialog>
