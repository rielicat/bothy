<script lang="ts">
  import { tick, untrack } from 'svelte';
  import Icon from '../../../components/Icon.svelte';
  import { Button, CodeBlock, Disclosure, LinkButton, SettingsSection, StatusLine, toast } from '../../../components/ui';
  import { api, errorText } from '../../../lib/api/client';
  import type { DoctorReport, Finding } from '../../../lib/api/types.gen';
  import { fullTime, relTime } from '../../../lib/format';
  import { router, safeNext } from '../../../lib/router.svelte';
  import { clock as ticker } from '../../../lib/stores/clock.svelte';
  import { bySeverity, describeFinding, needsAttention } from '../security/findings';

  let report = $state<DoctorReport | null>(null);
  let checkedAt = $state('');
  let running = $state(false);
  let failed = $state('');
  let fixing = $state(false);
  let fixError = $state('');
  let box: HTMLElement | undefined = $state();

  const findings = $derived(bySeverity(report?.findings));
  // Info findings are notes ("Telemetry is off"), so they never make it a problem.
  const problems = $derived(needsAttention(report?.findings).length);
  const since = $derived(relTime(checkedAt, { now: ticker.now }));
  const tone = (f: Finding) => (f.severity === 'critical' || f.severity === 'high' ? 'text-danger-text' : 'text-warning-text');
  const route = (f: Finding) => (f.fix && safeNext(f.fix, '') ? f.fix : '');

  async function run() {
    running = true;
    failed = '';
    try {
      report = await api.get<DoctorReport>('/api/doctor');
      checkedAt = new Date().toISOString();
    } catch (e) {
      failed = errorText(e);
    } finally {
      running = false;
    }
  }

  async function fix() {
    fixing = true;
    fixError = '';
    try {
      await api.post<DoctorReport>('/api/security-audit', { fix: true });
      toast('Fixed.');
      await run();
      // The fixed row is gone; focus stays in the section instead of falling to the page.
      await tick();
      box?.querySelector<HTMLElement>('[data-run-again]')?.focus();
    } catch (e) {
      fixError = errorText(e);
    } finally {
      fixing = false;
    }
  }

  $effect(() => {
    ticker.start();
    untrack(run);
  });
</script>

{#snippet again()}<Button size="sm" data-run-again loading={running && !!report} loadingLabel="Checking…" onclick={run}>Run again</Button>{/snippet}

<div bind:this={box}>
  <SettingsSection title="Health check" action={again}>
    {#if failed}
      <StatusLine class="py-3" tone="danger" text="The health check didn't run." details={failed} />
    {:else if !report}
      <StatusLine class="py-3" tone="progress" text="Checking…" />
    {:else}
      {#if !problems}
        <p class="border-b border-border py-3 text-body text-text" role="status">
          No problems found. <span class="text-muted">Last checked <time datetime={checkedAt} title={fullTime(checkedAt)}>{since}</time>.</span>
        </p>
      {/if}
      {#each findings as f, i (i)}
        {@const c = describeFinding(f)}
        <div class="flex items-start gap-3 border-b border-border py-3">
          <!-- Info findings are notes, not problems: no glyph, same indent. -->
          <span class="flex h-5 w-4 shrink-0 items-center">{#if f.severity !== 'info'}<Icon name="alert" class={tone(f)} />{/if}</span>
          <div class="min-w-0 flex-1">
            <p class="text-body text-text">{c.text}</p>
            {#if c.steps}<p class="mt-0.5 text-small text-muted">{c.steps}</p>{/if}
            {#if route(f)}<LinkButton class="mt-1 text-small" onclick={() => router.navigate(route(f))}>Open the setting</LinkButton>{/if}
            <Disclosure class="mt-1">
              <dl class="grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-1 text-small">
                <dt class="text-muted">Check</dt>
                <dd class="font-mono text-code text-text">{f.id}</dd>
                <dt class="text-muted">Severity</dt>
                <dd class="text-text">{f.severity}</dd>
                <dt class="text-muted">Reported</dt>
                <dd class="text-text">{f.title}</dd>
                {#if f.detail}
                  <dt class="text-muted">Detail</dt>
                  <dd class="text-text [overflow-wrap:anywhere]">{f.detail}</dd>
                {/if}
              </dl>
              {#if f.fix && !route(f)}<CodeBlock class="mt-2" label="Suggested fix" code={f.fix} wrap />{/if}
            </Disclosure>
          </div>
          {#if f.auto_fix}<Button size="sm" loading={fixing} loadingLabel="Fixing…" onclick={fix}>Fix</Button>{/if}
        </div>
      {/each}
    {/if}
  </SettingsSection>
  {#if fixError}<StatusLine class="mt-2" tone="danger" text="Bothy couldn't fix this." details={fixError} alert />{/if}
  {#if report && problems}<p class="mt-2 text-small text-muted">Last checked <time datetime={checkedAt} title={fullTime(checkedAt)}>{since}</time>.</p>{/if}
</div>
