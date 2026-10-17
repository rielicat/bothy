<script lang="ts">
  import type { Snippet } from 'svelte';
  import { foldable, summarize, type Phrase } from '../../lib/activity';
  import Icon from '../Icon.svelte';
  import ActivityLine from './ActivityLine.svelte';
  import PhraseText from './PhraseText.svelte';
  import type { StepView } from './steps';

  // One run's consecutive steps (§7.4). While the run works on them every step
  // is listed; once text follows or the run ends, finished steps fold into a
  // summary line. Waiting, refused and approval steps never fold, and a step
  // that didn't work stays visible under its summary.
  let { steps, live, approval }: { steps: StepView[]; live: boolean; approval: Snippet<[StepView]> } = $props();

  type Part = { key: string; one: StepView } | { key: string; fold: StepView[]; summary: Phrase; failed: number };

  const parts = $derived.by(() => {
    const out: Part[] = [];
    let run: StepView[] = [];
    const flush = () => {
      if (run.length > 1) {
        const { phrase, failed } = summarize(run);
        out.push({ key: `f${run[0].key}`, fold: run, summary: phrase, failed });
      } else if (run.length) out.push({ key: run[0].key, one: run[0] });
      run = [];
    };
    for (const s of steps) {
      if (!live && foldable(s.state) && !s.approval) {
        run.push(s);
        continue;
      }
      flush();
      out.push({ key: s.key, one: s });
    }
    flush();
    return out;
  });

  let open: Record<string, boolean> = $state({});

  // A refused request's decided line already says what didn't happen and
  // who decided, so the step's own "Didn't …" line would repeat it.
  const saidByDecision = (s: StepView) =>
    !!s.approval && (s.state === 'denied' || s.state === 'expired') && s.approval.status !== 'pending' && s.approval.status !== 'parked';
</script>

<div>
  {#each parts as p (p.key)}
    {#if 'one' in p}
      {#if !saidByDecision(p.one)}<ActivityLine step={p.one} />{/if}
      {#if p.one.approval}{@render approval(p.one)}{/if}
    {:else}
      <button
        type="button"
        class="flex min-h-8 w-full cursor-pointer items-start gap-2 rounded-md py-1.5 text-left text-body text-muted hover:text-text pointer-coarse:min-h-11 pointer-coarse:py-3"
        aria-expanded={!!open[p.key]}
        onclick={() => (open[p.key] = !open[p.key])}
      >
        <span class="flex h-5 w-4 shrink-0 items-center justify-center"><Icon name="chevron-right" size={12} class="transition-transform duration-160 {open[p.key] ? 'rotate-90' : ''}" /></span>
        {#if p.failed}<span class="flex h-5 shrink-0 items-center"><Icon name="alert" class="text-danger-text" /></span>{/if}
        <span class="min-w-0 [overflow-wrap:anywhere]"><PhraseText phrase={p.summary} /></span>
      </button>
      {#each p.fold as s (s.key)}
        {#if open[p.key] || s.state === 'failed'}<div class="pl-6"><ActivityLine step={s} /></div>{/if}
      {/each}
    {/if}
  {/each}
</div>
