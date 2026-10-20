<script lang="ts">
  import type { Snippet } from 'svelte';
  import { fill, prefix, type Phrase, type Phrases } from '../../lib/activity';
  import type { Approval } from '../../lib/api/types.gen';
  import { cardCopy, decidedLine, joinNames, withdrawn, type DecidedCopy } from '../../lib/cardcopy';
  import { clock, fullTime } from '../../lib/format';
  import { clock as tick } from '../../lib/stores/clock.svelte';
  import { session } from '../../lib/stores/session.svelte';
  import { appName, people as chatPeople } from '../chat/people.svelte';
  import PhraseText from '../chat/PhraseText.svelte';
  import Icon from '../Icon.svelte';
  import LinkButton from '../ui/LinkButton.svelte';
  import ApprovalCard from './ApprovalCard.svelte';
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
    phrases,
    afterStep = false,
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
    /** The chat transcript: the line says what the step's own phrases say, and "Show" opens the card under it. */
    phrases?: Phrases;
    /** In the transcript, right under the step's own line: "You approved it" instead of naming the action a second time. */
    afterStep?: boolean;
  } = $props();

  let el: HTMLElement | undefined = $state();
  let show = $state(false);

  // A grant lets through more than this one action ("running `rm` commands"),
  // so the line names the grant (§8.4). Its plain parts can quote a recipient.
  const grant = (): Phrase => cardCopy(approval).grant.map((s) => (typeof s === 'string' ? { k: 'name', v: s } : { k: 'code', v: s.code }));

  function transcriptLine(a: Approval, p: Phrases): Omit<DecidedCopy, 'segs'> & { phrase: Phrase } {
    const at = a.decided_at || undefined;
    const time = at && clock(at);
    // Ballots carry names a member can't look up and each voter's reason.
    const ballots = a.approvals?.votes ?? (a.votes ?? []).map((v) => ({ user: v.user_id, name: '', choice: v.choice, comment: v.comment }));
    const nameOf = (id?: string, name?: string) => (id && chatPeople.name(id) !== 'Someone' ? chatPeople.name(id) : name || chatPeople.name(id));
    const denial = ballots.find((v) => v.choice === 'deny');
    const by = denial ? nameOf(denial.user, denial.name) : nameOf(a.decided_by, a.decided_by_name);
    const via = a.decided_via && !['web', 'timeout', 'stop'].includes(a.decided_via) ? appName[a.decided_via] : '';
    const comment = denial?.comment || a.comment;
    const muted = (phrase: Phrase) => ({ glyph: 'close' as const, tone: 'muted' as const, phrase, time, at });
    if (a.status === 'expired') return muted(prefix("Expired: Bothy didn't ", p.ask));
    // A stop's notice below says why; the line says what didn't happen.
    if (a.status === 'cancelled') return muted(a.decided_via === 'stop' ? prefix("Cancelled: Bothy didn't ", p.ask) : fill('Cancelled by the person who asked', {}));
    if (withdrawn(a)) return muted(fill('Cancelled by {who}', { who: by === 'You' ? 'you' : by }));
    const denied = a.status === 'denied';
    let phrase: Phrase;
    if (via) phrase = fill(`${by} ${denied ? 'denied' : 'approved'} in ${via}`, {});
    else if (denied) phrase = [...prefix(`${by} denied `, p.ing), ...(comment ? fill(' · “{c}”', { c: comment }) : [])];
    else if (a.choice === 'allow_session') phrase = [...prefix(`${by} allowed `, grant()), { k: 'text', v: ' for this chat' }];
    else if (a.choice === 'allow_always') phrase = prefix(`${by} always ${by === 'You' ? 'allow' : 'allows'} `, grant());
    else {
      // A two-person rule: everyone who approved, not only the last.
      const yes = ballots.filter((v) => v.choice === 'allow_once');
      const who = yes.length > 1 ? joinNames(yes.map((v) => nameOf(v.user, v.name))) : by;
      phrase = afterStep ? fill(`${who} approved it`, {}) : prefix(`${who} approved `, p.ing);
    }
    return { glyph: denied ? 'close' : 'check', tone: denied ? 'danger' : 'accent', phrase, time, at };
  }

  const line: DecidedCopy & { phrase?: Phrase } = $derived(
    phrases
      ? { ...transcriptLine(approval, phrases), segs: [] }
      : decidedLine(approval, {
          me: viewer?.id ?? session.me?.user.id,
          name: (id) => people.name(id),
          votes: votes.byId[approval.id],
          list,
          now: tick.now,
        }),
  );
  const color = { accent: 'text-accent-text', danger: 'text-danger-text', muted: 'text-muted' };
  const toggle = $derived(phrases ? () => (show = !show) : onshow);
  const open = $derived(phrases ? show : expanded);

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
<div class={phrases ? 'text-small text-muted' : 'min-w-0'}>
  <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
  <p
    bind:this={el}
    class="flex items-start {phrases ? 'gap-2 py-1' : 'gap-1.5 text-small text-muted outline-offset-2'}"
    tabindex={phrases ? undefined : -1}
    data-decided={phrases ? undefined : approval.status}
  >
    <span class="flex h-[1.125rem] w-4 shrink-0 items-center justify-center"><Icon name={line.glyph} class={color[line.tone]} /></span>
    <!-- "· {time} · Show" stays together, so Show is never alone on a line and no line ends in "·". -->
    <span class="min-w-0 [overflow-wrap:anywhere]">
      {#if line.phrase}<PhraseText phrase={line.phrase} />{:else}<Words segs={line.segs} />{/if}{#if line.reason}{' · '}<span data-copy-ignore>“{line.reason}”</span>{/if}{#if line.at || toggle}{' '}<span class="whitespace-nowrap"
          >{#if line.at}· <time datetime={line.at} title={fullTime(line.at)}>{line.time}</time>{#if toggle}{' '}{/if}{/if}{#if toggle}· <LinkButton
              aria-expanded={open}
              onclick={toggle}>{open ? 'Hide' : 'Show'}</LinkButton
            >{/if}</span
        >{/if}
    </span>
  </p>
  {#if followUp && approval.status === 'allowed' && approval.action?.tool === 'release_deploy'}
    <div class={phrases ? 'pl-6' : 'mt-2'}><DeployFollowUp {approval} small={!!phrases} /></div>
  {/if}
  <!-- The line above already says the decision, so the card leaves its own out. -->
  {#if show}<div class="mt-2 mb-1"><ApprovalCard {approval} mode="readonly" decision={false} /></div>{/if}
</div>
{/if}
