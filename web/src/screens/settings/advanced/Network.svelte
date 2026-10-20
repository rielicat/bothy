<svelte:options runes />

<script lang="ts">
  import AdvancedField from '../../../components/AdvancedField.svelte';
  import { SettingsSection } from '../../../components/ui';
</script>

<SettingsSection title="Network">
  <AdvancedField path="server.allowed_hosts" label="Extra host names" type="list" placeholder="bothy.internal, 10.0.0.5" help="Other names this server answers to, besides its web address." />
  <AdvancedField path="egress.deny" label="Always blocked" type="list" help="Running code can never reach these, even when allowed elsewhere." />
  <AdvancedField
    path="egress.shared_tenant_suffixes"
    label="Shared hosting suffixes"
    type="list"
    help="Anyone can publish under these, so only admins can allow sites there."
  />
  <AdvancedField path="egress.ports" label="Ports" type="list" />
  <AdvancedField path="egress.allow_plain_http" label="Allow plain HTTP (port 80)" type="switch" />
  <AdvancedField path="egress.hold_for_approval" label="Hold unknown sites for approval" type="duration" help="How long running code waits for an answer; 0s blocks at once." />
  <AdvancedField path="egress.caps.session_up_mb" label="Upload limit per chat (MB)" type="number" />
  <AdvancedField path="egress.caps.session_down_mb" label="Download limit per chat (MB)" type="number" />
  <AdvancedField path="egress.dns.servers" label="DNS servers" type="list" placeholder="1.1.1.1, 9.9.9.9" />
  <AdvancedField path="egress.dns.max_ttl" label="Longest DNS cache time" type="duration" />
</SettingsSection>
