<script lang="ts">
  import { Select, SettingsRow } from '../../components/ui';
  import { refinePresets, refinePresetOf, type RefinePreset } from './modelHelpers';
  import { Saver, settingsConfig } from './store.svelte';

  // "How thorough" (§11.4, D38): one plain choice that sets drafts and rounds together.
  const saver = new Saver();
  const saved = $derived(refinePresetOf(Number(settingsConfig.value('agent.refine.drafts', 3)), Number(settingsConfig.value('agent.refine.depth', 2))));
  let value = $state<RefinePreset>('standard');
  $effect.pre(() => {
    value = saved;
  });

  const options = $derived([
    { value: 'standard', label: `Standard (${refinePresets.standard.drafts} drafts)` },
    { value: 'thorough', label: `Thorough (${refinePresets.thorough.drafts} drafts, slower)` },
    // Numbers set in Advanced that match neither choice stay as they are until someone picks one.
    ...(saved === 'custom' ? [{ value: 'custom', label: 'Custom (set in Advanced)' }] : []),
  ]);

  async function change(v: string) {
    if (v !== 'standard' && v !== 'thorough') return;
    value = v;
    const p = refinePresets[v];
    if (!(await saver.run(() => settingsConfig.save({ 'agent.refine.drafts': p.drafts, 'agent.refine.depth': p.depth })))) value = saved;
  }
</script>

<SettingsRow label="How thorough" status={saver.status} error={saver.error}>
  {#snippet children(c)}
    <Select id={c.id} bind:value {options} aria-describedby={c.describedBy} aria-invalid={c.invalid || undefined} onchange={(e: Event) => change((e.currentTarget as HTMLSelectElement).value)} />
  {/snippet}
</SettingsRow>
