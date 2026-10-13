<script lang="ts">
  import Icon from '../../components/Icon.svelte';
  import Section from '../../components/Section.svelte';
  import { api } from '../../lib/api/client';
  import type { DoctorReport, Finding } from '../../lib/api/types.gen';
  import { config, sourceLabel, type SchemaNode } from '../../lib/config.svelte';
  import { router, safeNext } from '../../lib/router.svelte';
  import { session } from '../../lib/stores/session.svelte';
  import { attempt, copyText, toasts } from '../../lib/stores/ui.svelte';
  import { toYaml } from '../../lib/yaml';

  let report = $state<DoctorReport | null>(null);
  let running = $state('');

  const schema = $derived(config.view?.schema as SchemaNode | undefined);
  const effective = $derived(toYaml(config.view?.entries ?? [], schema, (e) => (e.locked ? 'locked' : sourceLabel(e.source, session.me?.user.id))));
  const overlay = $derived(toYaml((config.view?.entries ?? []).filter((e) => e.source.layer === 'ui'), schema));
  const download = $derived(URL.createObjectURL(new Blob([overlay], { type: 'text/yaml' })));
  const sev: Record<string, string> = { critical: 'chip-bad', high: 'chip-bad', medium: 'chip-warn', low: 'chip-info', info: '' };
  const order = ['critical', 'high', 'medium', 'low', 'info'];
  const findings = $derived([...(report?.findings ?? [])].sort((a, b) => order.indexOf(a.severity) - order.indexOf(b.severity)));

  $effect(() => {
    const url = download;
    return () => URL.revokeObjectURL(url);
  });

  async function run(kind: 'doctor' | 'audit' | 'fix') {
    running = kind;
    try {
      report = kind === 'doctor' ? await api.get<DoctorReport>('/api/doctor') : await api.post<DoctorReport>('/api/security-audit', { fix: kind === 'fix' });
      if (report.fixed?.length) toasts.ok(`Fixed: ${report.fixed.join(', ')}`);
    } catch (e) {
      toasts.error(e);
    } finally {
      running = '';
    }
  }

  async function backup() {
    try {
      toasts.ok(`Backup written to ${(await api.post<{ path: string }>('/api/admin/backup')).path}`);
    } catch (e) {
      toasts.error(e);
    }
  }

  const isRoute = (f: Finding) => !!safeNext(f.fix, '');
</script>

<Section title="Health and security" desc="Doctor checks everything; the security audit checks the settings that affect safety.">
  {#snippet actions()}
    <button type="button" class="btn btn-sm" disabled={!!running} onclick={() => run('doctor')}>{running === 'doctor' ? 'Checking…' : 'Run doctor'}</button>
    <button type="button" class="btn btn-sm" disabled={!!running} onclick={() => run('audit')}>{running === 'audit' ? 'Auditing…' : 'Security audit'}</button>
  {/snippet}
  {#if !report}
    <p class="py-4 text-sm text-muted">Run a check to see findings.</p>
  {:else if !findings.length}
    <p class="alert-ok my-4"><Icon name="check" size={16} />No findings. Everything looks right.</p>
  {:else}
    {#each findings as f, i (i)}
      <div class="flex flex-wrap items-start gap-3 py-3.5 text-sm">
        <span class="chip {sev[f.severity] ?? ''}">{f.severity}</span>
        <div class="min-w-0 flex-1">
          <p class="font-medium">{f.title} <code class="text-xs font-normal text-faint">{f.id}</code></p>
          <p class="text-muted">{f.detail}</p>
          {#if f.fix && isRoute(f)}
            <a class="link text-xs" href={f.fix} onclick={router.link}>Open the setting</a>
          {:else if f.fix}
            <p class="mt-1 flex items-center gap-2"><code class="mono rounded bg-sunken px-2 py-1 text-xs">{f.fix}</code><button type="button" class="btn btn-ghost btn-icon btn-sm" aria-label="Copy fix" onclick={() => copyText(f.fix!)}><Icon name="copy" size={13} /></button></p>
          {/if}
        </div>
      </div>
    {/each}
    {#if findings.some((f) => f.auto_fix)}
      <div class="py-4"><button type="button" class="btn btn-primary btn-sm" disabled={!!running} onclick={() => run('fix')}>Fix what can be fixed automatically</button></div>
    {/if}
  {/if}
</Section>

<Section title="Maintenance">
  <div class="flex flex-wrap items-center gap-3 py-4 text-sm">
    <p class="min-w-0 flex-1">Back up the database, vault and config to <code>/data/backups</code>.</p>
    <button type="button" class="btn btn-sm" onclick={backup}><Icon name="download" size={14} />Back up now</button>
  </div>
  <div class="flex flex-wrap items-center gap-3 py-4 text-sm">
    <p class="min-w-0 flex-1">Restart to apply settings that need it. Running chats pause for a few seconds.</p>
    <button type="button" class="btn btn-sm" onclick={() => attempt(() => api.post('/api/admin/restart'), 'Restarting…')}><Icon name="refresh" size={14} />Restart</button>
  </div>
  <div class="flex flex-wrap items-center gap-3 py-4 text-sm">
    <p class="min-w-0 flex-1">Kill switch: stop every run, kill every sandbox box and pause schedules.</p>
    <button
      type="button"
      class="btn btn-sm {session.paused ? 'btn-primary' : 'btn-danger'}"
      onclick={async () => {
        const paused = !session.paused;
        if (await attempt(() => api.post('/api/admin/pause', { paused }))) session.paused = paused;
      }}><Icon name={session.paused ? 'play' : 'power'} size={14} />{session.paused ? 'Resume' : 'Stop everything'}</button
    >
  </div>
</Section>

<Section title="Effective config" desc="The merged result of defaults, bothy.yaml, environment and web UI changes, with where each value comes from.">
  {#snippet actions()}
    <a class="btn btn-sm" href={download} download="bothy.ui.yaml"><Icon name="download" size={14} />bothy.ui.yaml</a>
    <button type="button" class="btn btn-sm" onclick={() => copyText(effective)}><Icon name="copy" size={14} />Copy</button>
  {/snippet}
  <pre class="mono my-4 max-h-[36rem] overflow-auto rounded-lg bg-sunken p-4 text-xs leading-relaxed">{effective}</pre>
</Section>
