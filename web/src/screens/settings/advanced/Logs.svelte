<svelte:options runes />

<script lang="ts">
  import AdvancedField from '../../../components/AdvancedField.svelte';
  import { SettingsSection } from '../../../components/ui';
</script>

<SettingsSection title="Logs">
  <AdvancedField
    path="log.level"
    label="Log level"
    type="select"
    options={[
      { value: 'debug', label: 'Debug (everything)' },
      { value: 'info', label: 'Info' },
      { value: 'warn', label: 'Warnings and errors' },
      { value: 'error', label: 'Errors only' },
    ]}
  />
  <AdvancedField
    path="log.format"
    label="Log format"
    type="select"
    options={[
      { value: 'text', label: 'Text' },
      { value: 'json', label: 'JSON' },
    ]}
  />
</SettingsSection>
