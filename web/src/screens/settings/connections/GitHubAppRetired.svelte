<script lang="ts">
  import { Button, LinkButton, StatusLine, toast } from '../../../components/ui';
  import { api, errorText } from '../../../lib/api/client';
  import type { Failure } from './connections';
  import type { GitHubApp } from './types';

  // An app Bothy stopped using stays on GitHub, which Bothy never deletes it
  // from: this row says where to delete it until the admin dismisses it.
  let { app, ondismissed, class: cls = '' }: { app: GitHubApp; ondismissed: () => void; class?: string } = $props();

  let busy = $state(false);
  let problem = $state<Failure | null>(null);

  async function dismiss() {
    busy = true;
    problem = null;
    try {
      await api.del(`/api/github/apps/${encodeURIComponent(app.id)}`);
      toast('Dismissed.');
      ondismissed();
    } catch (e) {
      problem = { message: "Bothy couldn't dismiss this. Try again.", details: errorText(e) };
    } finally {
      busy = false;
    }
  }
</script>

<div class="border-b border-border py-3 {cls}">
  <div class="flex flex-wrap items-center justify-between gap-x-6 gap-y-2">
    <p class="min-w-0 text-body text-text">Bothy no longer uses {app.name}. Delete it on GitHub too.</p>
    <div class="flex items-center gap-4">
      <Button size="sm" href={app.settings_url} external>Open app settings</Button>
      <span class="text-small"><LinkButton disabled={busy} onclick={dismiss}>Dismiss</LinkButton></span>
    </div>
  </div>
  {#if problem}<StatusLine class="mt-2" tone="danger" alert text={problem.message} details={problem.details} />{/if}
</div>
