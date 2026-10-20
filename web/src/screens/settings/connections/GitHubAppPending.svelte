<script lang="ts">
  import { untrack } from 'svelte';
  import { Button, Dialog, StatusLine, toast } from '../../../components/ui';
  import { api, errorText } from '../../../lib/api/client';
  import type { Failure } from './connections';
  import FailureLine from './FailureLine.svelte';
  import type { GitHubApp, GitHubAppCheck } from './types';

  // An app GitHub created that isn't installed yet (§12.2).
  let {
    app,
    requested = false,
    failed = false,
    compact = false,
    oninstalled,
    onretired,
  }: {
    app: GitHubApp;
    /** A member asked an owner to approve the app on GitHub. */
    requested?: boolean;
    /** GitHub sent the browser back but the installation couldn't be confirmed. */
    failed?: boolean;
    /** Inside the manage view of the connection the app replaces. */
    compact?: boolean;
    oninstalled: () => void;
    onretired: () => void;
  } = $props();

  let checking = $state(false);
  let result = $state<{ tone: 'warning' | 'danger'; text: string; details?: string } | null>(null);
  let leaving = $state(false);
  let busy = $state(false);
  let problem = $state<Failure | null>(null);

  const owner = $derived(app.owner_type === 'Organization' ? app.owner : 'your account');

  async function check(quiet: boolean) {
    checking = !quiet;
    if (!quiet) result = null;
    try {
      const r = await api.post<GitHubAppCheck>(`/api/github/apps/${encodeURIComponent(app.id)}/check`);
      if (r.installed) oninstalled();
      else if (!quiet) result = { tone: 'warning', text: `${app.name} isn't installed yet.` };
    } catch (e) {
      if (!quiet) result = { tone: 'danger', text: "Bothy couldn't check with GitHub. Try again.", details: errorText(e) };
    } finally {
      checking = false;
    }
  }

  // One quiet check on load: the app may have been installed from GitHub's own pages.
  $effect(() => {
    void app.id;
    untrack(() => check(true));
  });

  async function startOver() {
    busy = true;
    problem = null;
    try {
      await api.post(`/api/github/apps/${encodeURIComponent(app.id)}/retire`);
      leaving = false;
      toast('Started over.');
      onretired();
    } catch (e) {
      problem = { message: "Bothy couldn't start over. Try again.", details: errorText(e) };
      leaving = false;
    } finally {
      busy = false;
    }
  }
</script>

<section class={compact ? 'mb-8' : 'mt-10'} aria-labelledby="gh-pending">
  <h2 id="gh-pending" class="section-title">Finish installing {app.name} on GitHub</h2>
  {#if requested}
    <p class="mt-1 text-body text-text">An owner of {owner} needs to approve {app.name} on GitHub. Once they have, choose Check again.</p>
  {:else if compact}
    <p class="mt-1 text-body text-text">Finish installing {app.name} on GitHub to switch Bothy to it.</p>
  {:else}
    <p class="mt-1 text-body text-text">GitHub created the app. Install it on {owner} and choose the repositories Bothy can use.</p>
  {/if}
  {#if failed}
    <StatusLine class="mt-3" tone="danger" alert text="Bothy couldn't confirm the app's installation with GitHub. Choose Check again, or install it again." />
  {/if}
  <div class="mt-4 flex flex-wrap items-center gap-x-3 gap-y-2">
    <Button variant="primary" href={app.install_url} external>Install on GitHub</Button>
    <Button loading={checking} loadingLabel="Checking…" onclick={() => check(false)}>Check again</Button>
  </div>
  {#if result}<StatusLine class="mt-3" tone={result.tone} alert={result.tone === 'danger'} text={result.text} details={result.details} />{/if}
  <FailureLine class="mt-3" failure={problem} />
  <div class="mt-3"><Button variant="danger-text" class="-ml-4" onclick={() => (leaving = true)}>Start over…</Button></div>
</section>

<Dialog bind:open={leaving} title="Start over?" initialFocus="cancel" {busy} cancel>
  <p class="text-body text-text">Bothy stops using {app.name} and deletes its keys. The app stays on GitHub until you delete it there.</p>
  {#snippet footer()}
    <Button variant="danger" loading={busy} loadingLabel="Starting over…" onclick={startOver}>Start over</Button>
  {/snippet}
</Dialog>
