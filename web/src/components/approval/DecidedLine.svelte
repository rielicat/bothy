<script lang="ts">
  import type { Snippet } from 'svelte';
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
    source,
  }: {
    approval: Approval;
    viewer?: Viewer;
    /** The Done tab: every line names what was decided, as the page's main text, with its time and source under it. */
    list?: boolean;
    /** In a list, where the request came from: the chat link or a chat app's context line. */
    source?: Snippet;
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

{#if list}
  <!-- The Done tab's main content reads as text, not muted transcript fine print; time, source and Show go on one meta line. -->
  <div class="min-w-0">
    <p bind:this={el} class="flex items-start gap-1.5 text-body text-text outline-offset-2" tabindex="-1" data-decided={approval.status}>
      <span class="flex h-5 w-4 shrink-0 items-center justify-center"><Icon name={line.glyph} class={color[line.tone]} /></span>
      <span class="min-w-0 [overflow-wrap:anywhere]"><Words segs={line.segs} />{#if line.reason}{' · '}<span data-copy-ignore>“{line.reason}”</span>{/if}</span>
    </p>
    {#if line.time || source || onshow}
      <!-- Each "·" stays with the word before it, so no line ends or starts on one. -->
      <p class="mt-0.5 pl-5.5 text-small text-muted [overflow-wrap:anywhere]">
        {#if line.time}<span class="whitespace-nowrap"
            ><time datetime={line.at} title={fullTime(line.at)}>{line.time}</time>{#if source}{' ·'}{:else if onshow}{' · '}<LinkButton aria-expanded={expanded} onclick={onshow}
                >{expanded ? 'Hide' : 'Show'}</LinkButton
              >{/if}</span
          >{/if}{#if source}{#if line.time}{' '}{/if}{@render source()}{/if}{#if onshow && (source || !line.time)}{#if source}{' '}{/if}<span class="whitespace-nowrap"
            >{#if source}·{' '}{/if}<LinkButton aria-expanded={expanded} onclick={onshow}>{expanded ? 'Hide' : 'Show'}</LinkButton></span
          >{/if}
      </p>
    {/if}
    {#if followUp && approval.status === 'allowed' && approval.action?.tool === 'release_deploy'}
      <div class="mt-1 pl-5.5"><DeployFollowUp {approval} small /></div>
    {/if}
  </div>
{:else}
<div class="min-w-0">
  <p bind:this={el} class="flex items-start gap-1.5 text-small text-muted outline-offset-2" tabindex="-1" data-decided={approval.status}>
    <span class="flex h-[1.125rem] w-4 shrink-0 items-center justify-center"><Icon name={line.glyph} class={color[line.tone]} /></span>
    <!-- "· {time} · Show" stays together, so Show is never alone on a line and no line ends in "·". -->
    <span class="min-w-0 [overflow-wrap:anywhere]">
      <Words segs={line.segs} />{#if line.reason}{' · '}<span data-copy-ignore>“{line.reason}”</span>{/if}{#if line.time || onshow}{' '}<span class="whitespace-nowrap"
          >{#if line.time}· <time datetime={line.at} title={fullTime(line.at)}>{line.time}</time>{#if onshow}{' '}{/if}{/if}{#if onshow}· <LinkButton
              aria-expanded={expanded}
              onclick={onshow}>{expanded ? 'Hide' : 'Show'}</LinkButton
            >{/if}</span
        >{/if}
    </span>
  </p>
  {#if followUp && approval.status === 'allowed' && approval.action?.tool === 'release_deploy'}
    <div class="mt-2"><DeployFollowUp {approval} /></div>
  {/if}
</div>
{/if}
