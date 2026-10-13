<script lang="ts">
  import ConfigField from '../../components/ConfigField.svelte';
  import SandboxStatus from '../../components/SandboxStatus.svelte';
  import Section from '../../components/Section.svelte';
  import { api } from '../../lib/api/client';
  import type { EgressRecord, List } from '../../lib/api/types.gen';
  import { ago } from '../../lib/format';
  import { siteGrant } from '../../lib/grants';
  import { attempt } from '../../lib/stores/ui.svelte';

  let blocked = $state<EgressRecord[]>([]);

  $effect(() => {
    api.get<List<EgressRecord>>('/api/egress?limit=100').then((l) => {
      // Every connection is logged; one row per destination is enough here.
      const seen = new Map<string, EgressRecord>();
      for (const e of l.items ?? []) if ((e.decision === 'deny' || e.decision === 'held_deny') && !seen.has(`${e.host}:${e.port}`)) seen.set(`${e.host}:${e.port}`, e);
      blocked = [...seen.values()].slice(0, 8);
    }, () => {});
  });

  // The API creates principal-scope grants only (POST /api/grants); a narrow
  // domain grant is the closest to "allow for this session".
  async function allow(e: EgressRecord) {
    const grant = siteGrant(e.principal_id ?? '', e.host, e.port);
    if (await attempt(() => api.post('/api/grants', grant), `Bothy may now reach ${grant.value}.`))
      blocked = blocked.filter((x) => x.host !== e.host || x.port !== e.port);
  }
</script>

<Section title="Sandbox" desc="Code tools run in throwaway containers with no network except Bothy's egress proxy.">
  <SandboxStatus quota />
  <ConfigField path="sandbox.backend" label="Backend" help="auto tries the sandboxd sidecar, then bwrap, then turns code tools off. Never falls back to the host." />
  <ConfigField path="sandbox.profile" label="Box image" options={[['base', 'Base (Python, shell tools)'], ['node', 'Node (adds Node.js)']]} />
  <ConfigField path="sandbox.limits.memory_mb" label="Memory per box (MB)" />
  <ConfigField path="sandbox.limits.cpus" label="CPUs per box" step={0.25} />
  <ConfigField path="sandbox.limits.pids" label="Processes per box" />
  <ConfigField path="sandbox.exec_timeout" label="Command timeout" />
  <ConfigField path="sandbox.workspace_quota_mb" label="Workspace quota (MB)" />
  <ConfigField path="sandbox.idle_timeout" label="Stop idle boxes after" />
</Section>

<Section title="Egress" desc="Boxes reach only these destinations, through a proxy that logs every connection.">
  <ConfigField path="egress.allow" label="Allowed sites" help="host or host:port; *.example.com allows subdomains." />
  <ConfigField path="egress.deny" label="Always blocked" />
  <ConfigField path="egress.presets.python_packages" label="Allow PyPI" help="pypi.org and files.pythonhosted.org for pip install." />
  <ConfigField path="egress.presets.npm_packages" label="Allow npm" help="registry.npmjs.org for npm install." />
  <ConfigField path="egress.ports" label="Ports" help="443 by default." />
  <ConfigField path="egress.allow_plain_http" label="Allow plain HTTP (port 80)" />
  <ConfigField path="egress.hold_for_approval" label="Hold unknown sites for approval" help="How long a box waits for your answer; 0s blocks immediately." />
  <ConfigField path="egress.caps.session_up_mb" label="Upload cap per chat (MB)" />
  <ConfigField path="egress.caps.session_down_mb" label="Download cap per chat (MB)" />
  <ConfigField path="egress.shared_tenant_suffixes" label="Shared hosting suffixes" help="Anyone can host content there, so only admins may allow them." />
</Section>

<Section title="Recently blocked" desc="Allow a site in one click; it becomes a narrow, revocable permission.">
  {#each blocked as e (e.id)}
    <div class="flex flex-wrap items-center gap-3 py-3 text-sm">
      <span class="mono min-w-0 flex-1 truncate">{e.host}:{e.port}</span>
      <span class="text-xs text-muted">{e.reason || e.decision} · {ago(e.at)}</span>
      {#if e.principal_id}<button type="button" class="btn btn-sm" onclick={() => allow(e)}>Allow this site</button>{/if}
    </div>
  {:else}
    <p class="py-4 text-sm text-muted">Nothing blocked recently.</p>
  {/each}
</Section>
