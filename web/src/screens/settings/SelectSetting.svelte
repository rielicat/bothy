<script lang="ts">
  import { Select, SettingsRow, type SelectOption } from '../../components/ui';
  import { helpFor, Saver, settingsConfig } from './store.svelte';

  // A choice that saves as soon as it changes (§11.1).
  let { label, path, help, options, fallback = '' }: { label: string; path: string; help?: string; options: SelectOption[]; fallback?: string } = $props();

  const saver = new Saver();
  const saved = $derived(String(settingsConfig.value(path, fallback)));
  let value = $state('');
  $effect.pre(() => {
    value = saved;
  });

  async function change(v: string) {
    value = v;
    if (!(await saver.run(() => settingsConfig.save({ [path]: v })))) value = saved;
  }
</script>

<SettingsRow {label} help={helpFor(path, help)} status={saver.status} error={saver.error}>
  {#snippet children(c)}
    <Select id={c.id} bind:value {options} aria-describedby={c.describedBy} aria-invalid={c.invalid || undefined} onchange={(e: Event) => change((e.currentTarget as HTMLSelectElement).value)} />
  {/snippet}
</SettingsRow>
