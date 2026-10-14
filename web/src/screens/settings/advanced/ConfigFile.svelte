<script lang="ts">
  import { CodeBlock, Notice, SettingsSection, StatusLine } from '../../../components/ui';
  import { api, errorText } from '../../../lib/api/client';
  import { config, sourceLabel, type SchemaNode } from '../../../lib/config.svelte';
  import { session } from '../../../lib/stores/session.svelte';
  import { toYaml } from '../../../lib/yaml';

  const schema = $derived(config.view?.schema as SchemaNode | undefined);
  const entries = $derived(config.view?.entries ?? []);
  const effective = $derived(toYaml(entries, schema, (e) => (e.locked ? 'locked' : sourceLabel(e.source, session.me?.user.id))));
  // The download is the web UI's own layer, the file Bothy writes changes to.
  const overlay = $derived(toYaml(entries.filter((e) => e.source.layer === 'ui'), schema));
  const download = $derived(URL.createObjectURL(new Blob([overlay], { type: 'text/yaml' })));
  const errors = $derived(config.view?.errors ?? []);
  const restart = $derived(config.view?.restart_required ?? []);

  const safeMode = "Bothy started with safe defaults because some settings couldn't be loaded.";
  const fellBack = "Some settings couldn't be loaded, so they use the value from the layer below.";

  let restarting = $state(false);
  let restartError = $state('');
  let restarted = $state(false);

  $effect(() => {
    const url = download;
    return () => URL.revokeObjectURL(url);
  });

  async function restartNow() {
    restarting = true;
    restartError = '';
    try {
      await api.post('/api/admin/restart');
      restarted = true;
    } catch (e) {
      restartError = errorText(e);
    } finally {
      restarting = false;
    }
  }
</script>

<SettingsSection title="Configuration file">
  {#if config.view?.safe_mode || errors.length}
    <Notice tone="danger" class="mb-3" text={config.view?.safe_mode ? safeMode : fellBack} />
    {#if errors.length}
      <CodeBlock class="mb-3" label="Settings that couldn't be loaded" code={errors.map((e) => e.message).join('\n')} wrap />
    {/if}
  {/if}
  {#if restart.length}
    {#if restarting || restarted}
      <StatusLine class="mb-3" tone="progress" text={restarted ? "Bothy is restarting. This page reconnects when it's back." : 'Restarting…'} />
    {:else}
      <Notice class="mb-3" text="Restart Bothy to apply changes to {restart.join(', ')}." action={{ label: 'Restart Bothy', onclick: restartNow }} />
    {/if}
    {#if restartError}<StatusLine class="mb-3" tone="danger" text="Bothy didn't restart." details={restartError} alert />{/if}
  {/if}
  <CodeBlock code={effective} maxLines={24} label="Effective configuration" />
  <!-- A plain link: Button has no download attribute, and the file is a blob URL. -->
  <a class="btn btn-secondary mt-3" href={download} download="bothy.ui.yaml">Download bothy.ui.yaml</a>
</SettingsSection>
