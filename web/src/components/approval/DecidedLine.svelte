<script lang="ts">
  import type { Approval } from '../../lib/api/types.gen';
  import { decidedLine } from '../../lib/cardcopy';
  import { fullTime } from '../../lib/format';
  import { clock as tick } from '../../lib/stores/clock.svelte';
  import { session } from '../../lib/stores/session.svelte';
  import Icon from '../Icon.svelte';
  import LinkButton from '../ui/LinkButton.svelte';
  import DeployFollowUp from './DeployFollowUp.svelte';
  import { people } from './people.svelte';
  import type { Viewer } from './types';
  import { votes } from './votes.svelte';
  import Words from './Words.svelte';

  // A decided approval as one line (§8.4), kept in the transcript and listed
  // in the Done tab. "Show" re-opens the card read-only.
  let {
    approval,
    viewer,
    list = false,
    expanded,
    onshow,
    followUp = true,
  }: {
    approval: Approval;
    viewer?: Viewer;
    /** The Done tab: every line names what was decided. */
    list?: boolean;
    /** Whether the read-only card is open, for aria-expanded on "Show". */
    expanded?: boolean;
    onshow?: () => void;
    /** Follow an approved deploy until it finishes (§8.7). */
    followUp?: boolean;
  } = $props();

  let el: HTMLElement | undefined = $state();

  const line = $derived(
    decidedLine(approval, {
      me: viewer?.id ?? session.me?.user.id,
      name: (id) => people.name(id),
      votes: votes.byId[approval.id],
      list,
      now: tick.now,
    }),
  );
  const color = { accent: 'text-accent-text', danger: 'text-danger-text', muted: 'text-muted' };

  /** Moves focus here, for after a decision removed the buttons that had it. */
  export function focus() {
    el?.focus();
  }
</script>

<div class="min-w-0">
  <p bind:this={el} class="flex items-start gap-1.5 text-small text-muted outline-offset-2" tabindex="-1" data-decided={approval.status}>
    <span class="flex h-[1.125rem] w-4 shrink-0 items-center justify-center"><Icon name={line.glyph} class={color[line.tone]} /></span>
    <span class="min-w-0 [overflow-wrap:anywhere]">
      <Words segs={line.segs} />{#if line.reason}{' · '}<span data-copy-ignore>“{line.reason}”</span>{/if}{#if line.time}{' · '}<time
          datetime={line.at}
          title={fullTime(line.at)}>{line.time}</time
        >{/if}{#if onshow}{' · '}<LinkButton aria-expanded={expanded} onclick={onshow}>{expanded ? 'Hide' : 'Show'}</LinkButton>{/if}
    </span>
  </p>
  {#if followUp && approval.status === 'allowed' && approval.action?.tool === 'release_deploy'}
    <div class={list ? 'mt-1' : 'mt-2'}><DeployFollowUp {approval} small={list} /></div>
  {/if}
</div>
