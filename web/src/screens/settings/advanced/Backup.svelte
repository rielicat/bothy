<script lang="ts">
  import { Button, SettingsSection, StatusLine } from '../../../components/ui';
  import { api, errorText } from '../../../lib/api/client';
  import { fullTime } from '../../../lib/format';
  import { relLower } from '../security/page.svelte';

  // The gateway reports a backup only when it makes one (no "last backup" read
  // yet, flagged for the server), so the line starts generic.
  let last = $state<{ at: string; path: string } | null>(null);
  let running = $state(false);
  let error = $state('');

  async function backup() {
    running = true;
    error = '';
    try {
      const r = await api.post<{ path: string }>('/api/admin/backup');
      last = { at: new Date().toISOString(), path: r.path };
    } catch (e) {
      error = errorText(e);
    } finally {
      running = false;
    }
  }
</script>

<SettingsSection title="Backup" bare>
  <div class="list-card flex min-h-14 flex-wrap items-center justify-between gap-x-6 gap-y-2 px-4 py-2">
    <p class="text-body text-text">
      {#if last}Last backup: <time datetime={last.at} title={fullTime(last.at)}>{relLower(last.at)}</time>{:else}Back up the database, saved keys and settings.{/if}
    </p>
    <Button loading={running} loadingLabel="Backing up…" onclick={backup}>Back up now</Button>
  </div>
  {#if last}<StatusLine class="mt-2" tone="success" text="Saved to {last.path}." />{/if}
  {#if error}<StatusLine class="mt-2" tone="danger" text="The backup didn't finish." details={error} alert />{/if}
</SettingsSection>
