<script lang="ts">
  import AdvancedField from '../../../components/AdvancedField.svelte';
  import { SettingsSection } from '../../../components/ui';
  import { api } from '../../../lib/api/client';
  import type { SandboxInfo } from '../../../lib/api/types.gen';
  import { fullTime, relTime } from '../../../lib/format';

  let info = $state<SandboxInfo | null>(null);
  const st = $derived(info?.status);
  const imageNames: Record<string, string> = { base: 'Base', node: 'Node' };
  const images = $derived((st?.profiles ?? []).map((p) => imageNames[p] ?? p).join(', '));

  $effect(() => {
    api.get<SandboxInfo>('/api/sandbox').then((i) => (info = i), () => {});
  });
</script>

<SettingsSection title="Running code">
  {#if st}
    <dl class="grid grid-cols-[minmax(0,1fr)_minmax(0,1.4fr)] gap-x-6 border-b border-border py-3 text-body sm:grid-cols-[minmax(0,1fr)_minmax(0,20rem)]">
      <dt class="py-1 text-muted">Status</dt>
      <dd class="py-1 {st.available ? 'text-text' : 'text-danger-text'}">{st.available ? 'Isolated workspaces are on' : `Off: ${st.reason || 'no isolated workspace'}`}</dd>
      <dt class="py-1 text-muted">Backend</dt>
      <dd class="py-1 font-mono text-code text-text">{st.backend || 'none'}</dd>
      {#if st.runtime}
        <dt class="py-1 text-muted">Runtime</dt>
        <dd class="py-1 font-mono text-code text-text">{st.runtime}{st.rootless ? ' (rootless)' : ''}</dd>
      {/if}
      {#if st.engine}
        <dt class="py-1 text-muted">Engine</dt>
        <dd class="py-1 font-mono text-code text-text [overflow-wrap:anywhere]">{st.engine}</dd>
      {/if}
      {#each st.self_test ?? [] as t, i (i)}
        <dt class="py-1 text-muted">Self-test: {t.name}</dt>
        <dd class="py-1 {t.ok ? 'text-text' : 'text-danger-text'}">{t.ok ? 'Passed' : `Failed${t.detail ? `: ${t.detail}` : ''}`}</dd>
      {/each}
      {#if images}
        <dt class="py-1 text-muted">Workspace images</dt>
        <dd class="py-1 text-text">{images}</dd>
      {/if}
      {#if info?.quota_mb}
        <dt class="py-1 text-muted">Your workspace</dt>
        <dd class="py-1 text-text tabular-nums">{info.used_mb} MB of {info.quota_mb} MB</dd>
      {/if}
      <dt class="py-1 text-muted">Last checked</dt>
      <dd class="py-1 text-text"><time datetime={st.checked_at} title={fullTime(st.checked_at)}>{relTime(st.checked_at)}</time></dd>
    </dl>
  {/if}
  <AdvancedField
    path="sandbox.backend"
    label="Backend"
    type="select"
    options={[
      { value: 'auto', label: 'Automatic' },
      { value: 'sandboxd', label: 'sandboxd (containers)' },
      { value: 'bwrap', label: 'bwrap' },
      { value: 'none', label: 'Off' },
    ]}
    help="Automatic uses the best one available, or turns running code off."
  />
  <AdvancedField
    path="sandbox.profile"
    label="Workspace image"
    type="select"
    options={[
      { value: 'auto', label: 'Automatic' },
      { value: 'base', label: 'Base (Python and shell tools)' },
      { value: 'node', label: 'Node (adds Node.js)' },
    ]}
    help="Automatic uses Node when this server has it, and Base when it doesn't."
  />
  <AdvancedField path="sandbox.limits.memory_mb" label="Memory per workspace (MB)" type="number" />
  <AdvancedField path="sandbox.limits.cpus" label="CPUs per workspace" type="number" />
  <AdvancedField path="sandbox.limits.pids" label="Processes per workspace" type="number" />
  <AdvancedField path="sandbox.exec_timeout" label="Command time limit" type="duration" />
  <AdvancedField path="sandbox.output_kb" label="Output kept per command (KB)" type="number" />
  <AdvancedField path="sandbox.workspace_quota_mb" label="Files per workspace (MB)" type="number" />
  <AdvancedField path="sandbox.idle_timeout" label="Stop idle workspaces after" type="duration" />
</SettingsSection>
