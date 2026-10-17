<script lang="ts">
  import type { Snippet } from 'svelte';
  import { foldable, summarize, type Phrase } from '../../lib/activity';
  import { decidable, isOpen } from '../../lib/cardcopy';
  import { session } from '../../lib/stores/session.svelte';
  import { votes } from '../approval/votes.svelte';
  import Icon from '../Icon.svelte';
  import ActivityLine from './ActivityLine.svelte';
  import PhraseText from './PhraseText.svelte';
  import type { StepView } from './steps';

  // One run's consecutive steps (§7.4). While the run works on them every step
  // is listed; once text follows or the run ends, finished steps fold into a
  // summary line. Waiting, failed, refused and approval steps never fold.
  // `hide` names a step a notice below already says in full (code that can't
  // run on this server). `approval` gets whether the step's own line sits
  // right above it, so a decided line can drop what that line already says.
  let { steps, live, approval, hide }: { steps: StepView[]; live: boolean; approval: Snippet<[StepView, boolean]>; hide?: string } = $props();

  type Part = { key: string; one: StepView } | { key: string; fold: StepView[]; summary: Phrase };

  const parts = $derived.by(() => {
    const out: Part[] = [];
    let run: StepView[] = [];
    const flush = () => {
      if (run.length > 1) out.push({ key: `f${run[0].key}`, fold: run, summary: summarize(run) });
      else if (run.length) out.push({ key: run[0].key, one: run[0] });
      run = [];
    };
    for (const s of steps) {
      if (s.key === hide) continue;
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

  const me = $derived(session.me ? { id: session.me.user.id, role: session.me.user.role } : undefined);
  // The pending card renders right below its waiting line, which then only says whose approval it waits for.
  const card = (s: StepView): 'yours' | 'theirs' | undefined =>
    s.approval && isOpen(s.approval) ? (decidable(s.approval, me, votes.byId[s.approval.id]) ? 'yours' : 'theirs') : undefined;
</script>

<div>
  {#each parts as p (p.key)}
    {#if 'one' in p}
      {@const said = saidByDecision(p.one)}
      {#if !said}<ActivityLine step={p.one} card={card(p.one)} />{/if}
      {#if p.one.approval}{@render approval(p.one, !said)}{/if}
    {:else}
      <button
        type="button"
        class="flex min-h-8 w-full cursor-pointer items-start gap-2 rounded-md py-1.5 text-left text-body text-muted hover:text-text pointer-coarse:min-h-11 pointer-coarse:py-3"
        aria-expanded={!!open[p.key]}
        onclick={() => (open[p.key] = !open[p.key])}
      >
        <span class="flex h-5 w-4 shrink-0 items-center justify-center"><Icon name="chevron-right" size={12} class="transition-transform duration-160 {open[p.key] ? 'rotate-90' : ''}" /></span>
        <span class="min-w-0 [overflow-wrap:anywhere]"><PhraseText phrase={p.summary} /></span>
      </button>
      {#if open[p.key]}
        {#each p.fold as s (s.key)}<div class="pl-6"><ActivityLine step={s} /></div>{/each}
      {/if}
    {/if}
  {/each}
</div>
