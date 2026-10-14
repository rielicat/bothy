<script lang="ts">
  import { SettingsRow, Switch } from '../../components/ui';
  import { helpFor, Saver, settingsConfig } from './store.svelte';

  // An on/off setting; the switch stays on the label line on phones too (§11.1).
  let { label, path, help }: { label: string; path: string; help?: string } = $props();

  const saver = new Saver();
  const on = $derived(settingsConfig.value<unknown>(path) === true);
  let checked = $state(false);
  $effect.pre(() => {
    checked = on;
  });

  async function change(v: boolean) {
    // A failed save puts the switch back, so it never shows a value that isn't saved.
    if (!(await saver.run(() => settingsConfig.save({ [path]: v })))) checked = on;
  }
</script>

<SettingsRow {label} help={helpFor(path, help)} status={saver.status} error={saver.error} inline>
  {#snippet children(c)}
    <Switch id={c.id} {label} hideLabel bind:checked aria-describedby={c.describedBy} onchange={change} />
  {/snippet}
</SettingsRow>
