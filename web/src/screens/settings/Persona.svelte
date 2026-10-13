<script lang="ts">
  import ConfigField from '../../components/ConfigField.svelte';
  import Markdown from '../../components/Markdown.svelte';
  import Section from '../../components/Section.svelte';
  import { api } from '../../lib/api/client';
  import { session } from '../../lib/stores/session.svelte';
  import { attempt } from '../../lib/stores/ui.svelte';

  let soul = $state('');
  let saved = $state('');
  let constitution = $state<{ version: string; content: string } | null>(null);
  const size = $derived(new TextEncoder().encode(soul).length);

  $effect(() => {
    api.get<{ content: string }>('/api/persona').then((p) => (soul = saved = p.content), () => {});
    api.get<{ version: string; content: string }>('/api/constitution').then((c) => (constitution = c), () => {});
  });

  async function save() {
    if (await attempt(() => api.put('/api/persona', { content: soul }), 'Saved. Bothy uses it from the next message.')) saved = soul;
  }
</script>

<Section title="SOUL.md" desc="Bothy's name, voice and habits. It cannot override the constitution.">
  <div class="py-4">
    {#if session.isAdmin}
      <label for="soul" class="sr-only">SOUL.md</label>
      <textarea id="soul" class="input mono min-h-64" bind:value={soul}></textarea>
      <div class="mt-2 flex items-center gap-3">
        <span class="text-xs tabular-nums {size > 16384 ? 'text-bad' : 'text-muted'}">{size.toLocaleString()} / 16,384 bytes</span>
        <div class="flex-1"></div>
        <button type="button" class="btn btn-primary btn-sm" disabled={soul === saved || size > 16384} onclick={save}>Save</button>
      </div>
    {:else}
      <Markdown text={soul} />
    {/if}
  </div>
</Section>

{#if session.isAdmin}
  <Section title="Standing rules" desc="Lines added to the constitution block for every run. Only admins can change them, never the agent.">
    <ConfigField path="agent.standing_rules" label="Rules" />
  </Section>
{/if}

<Section title="Constitution{constitution ? ` · v${constitution.version}` : ''}" desc="Built in and read-only. It stays in context through every summary.">
  <div class="max-h-[32rem] overflow-y-auto py-4">
    {#if constitution}<Markdown text={constitution.content} />{/if}
  </div>
</Section>
