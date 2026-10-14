<script lang="ts">
  import { untrack } from 'svelte';
  import { Page } from '../../../components/ui';
  import { config } from '../../../lib/config.svelte';
  import { session } from '../../../lib/stores/session.svelte';
  import { settingsBack } from '../security/page.svelte';
  import PageState from '../security/PageState.svelte';
  import Backup from './Backup.svelte';
  import ConfigFile from './ConfigFile.svelte';
  import HealthCheck from './HealthCheck.svelte';
  import Limits from './Limits.svelte';
  import Logs from './Logs.svelte';
  import ModelPrices from './ModelPrices.svelte';
  import Network from './Network.svelte';
  import Rules from './Rules.svelte';
  import RunningCode from './RunningCode.svelte';
  import SafetyRules from './SafetyRules.svelte';

  // docs/design/ui.md §11.7: the one place config keys, provenance and raw detail live.
  function load() {
    void config.load(true);
  }

  $effect(() => {
    if (session.isAdmin) untrack(load);
  });
</script>

<Page title="Advanced" back={settingsBack()}>
  {#if !session.isAdmin}
    <PageState kind="forbidden" />
  {:else if !config.view && config.error}
    <PageState kind="failed" onretry={load} />
  {:else if !config.view}
    <PageState kind="loading" />
  {:else}
    <p class="text-body text-muted">Settings for troubleshooting and fine-tuning. Most teams never need these.</p>
    <div class="mt-10">
      <HealthCheck />
      <ConfigFile />
      <Backup />
      <Rules />
      <Limits />
      <RunningCode />
      <Network />
      <ModelPrices />
      <SafetyRules />
      <Logs />
    </div>
  {/if}
</Page>
