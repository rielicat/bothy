<script lang="ts">
  import Markdown from '../../../components/Markdown.svelte';
  import { SettingsSection, StatusLine } from '../../../components/ui';
  import { api, errorText } from '../../../lib/api/client';

  let text = $state('');
  let failed = $state('');

  $effect(() => {
    api.get<{ version: string; content: string }>('/api/constitution').then(
      (c) => (text = c.content),
      (e) => (failed = errorText(e)),
    );
  });
</script>

<SettingsSection title="Built-in safety rules" bare>
  <p class="text-small text-muted">These apply to every chat and can't be changed.</p>
  {#if failed}
    <StatusLine class="mt-3" tone="danger" text="The safety rules didn't load." details={failed} />
  {:else if text}
    <Markdown class="mt-3 text-reading" {text} />
  {/if}
</SettingsSection>
