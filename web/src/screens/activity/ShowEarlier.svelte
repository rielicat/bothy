<script lang="ts">
  import { Button } from '../../components/ui';

  // The next page of a list. A page that didn't load says so here, where it was asked for:
  // the list above stays as it was, so the page's own load error would never show.
  let { load }: { load: () => Promise<unknown> } = $props();

  let busy = $state(false);
  let failed = $state(false);

  async function more() {
    busy = true;
    failed = false;
    try {
      await load();
    } catch {
      failed = true;
    } finally {
      busy = false;
    }
  }
</script>

<Button class="mt-6" size="sm" loading={busy} loadingLabel="Loading…" onclick={more}>Show earlier</Button>
{#if failed}<p class="field-error" role="alert">Couldn't load more. Check your connection and try again.</p>{/if}
