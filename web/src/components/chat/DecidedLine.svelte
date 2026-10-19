<script lang="ts">
  import { fill, prefix, type Phrase, type Phrases } from '../../lib/activity';
  import { cardCopy, joinNames, withdrawn } from '../../lib/cardcopy';
  import type { Approval } from '../../lib/api/types.gen';
  import { clock, fullTime } from '../../lib/format';
  import DeployFollowUp from '../approval/DeployFollowUp.svelte';
  import Icon from '../Icon.svelte';
  import LinkButton from '../ui/LinkButton.svelte';
  import InlineApproval from './InlineApproval.svelte';
  import { appName, people } from './people.svelte';
  import PhraseText from './PhraseText.svelte';

  // The one-line record a decided card leaves in the transcript (§8.4). Local
  // until C4's DecidedLine is exported from components/approval.
  // `afterStep`: the step's own line sits right above, so an approval says
  // "You approved it" instead of naming the action a second time.
  let { approval, phrases, afterStep = false }: { approval: Approval; phrases: Phrases; afterStep?: boolean } = $props();

  let show = $state(false);

  // A grant lets through more than this one action ("running `rm` commands"),
  // so the line names the grant (§8.4). Its plain parts can quote a recipient.
  const grant = (): Phrase => cardCopy(approval).grant.map((s) => (typeof s === 'string' ? { k: 'name', v: s } : { k: 'code', v: s.code }));

  const line = $derived.by((): { phrase: Phrase; tone: string; icon: 'check' | 'close' } => {
    const a = approval;
    // Ballots carry names a member can't look up and each voter's reason.
    const ballots = a.approvals?.votes ?? (a.votes ?? []).map((v) => ({ user: v.user_id, name: '', choice: v.choice, comment: v.comment }));
    const nameOf = (id?: string, name?: string) => (id && people.name(id) !== 'Someone' ? people.name(id) : name || people.name(id));
    const denial = ballots.find((v) => v.choice === 'deny');
    const by = denial ? nameOf(denial.user, denial.name) : nameOf(a.decided_by, a.decided_by_name);
    const via = a.decided_via && !['web', 'timeout', 'stop'].includes(a.decided_via) ? appName[a.decided_via] : '';
    const comment = denial?.comment || a.comment;
    if (a.status === 'expired') return { phrase: prefix("Expired: Bothy didn't ", phrases.ask), tone: 'text-muted', icon: 'close' };
    // A stop's notice below says why; the line says what didn't happen.
    if (a.status === 'cancelled')
      return { phrase: a.decided_via === 'stop' ? prefix("Cancelled: Bothy didn't ", phrases.ask) : fill('Cancelled by the person who asked', {}), tone: 'text-muted', icon: 'close' };
    if (withdrawn(a)) return { phrase: fill('Cancelled by {who}', { who: by === 'You' ? 'you' : by }), tone: 'text-muted', icon: 'close' };
    const denied = a.status === 'denied';
    let phrase: Phrase;
    if (via) phrase = fill(`${by} ${denied ? 'denied' : 'approved'} in ${via}`, {});
    else if (denied) phrase = [...prefix(`${by} denied `, phrases.ing), ...(comment ? fill(' · “{c}”', { c: comment }) : [])];
    else if (a.choice === 'allow_session') phrase = [...prefix(`${by} allowed `, grant()), { k: 'text', v: ' for this chat' }];
    else if (a.choice === 'allow_always') phrase = prefix(`${by} always ${by === 'You' ? 'allow' : 'allows'} `, grant());
    else {
      // A two-person rule: everyone who approved, not only the last.
      const yes = ballots.filter((v) => v.choice === 'allow_once');
      const who = yes.length > 1 ? joinNames(yes.map((v) => nameOf(v.user, v.name))) : by;
      phrase = afterStep ? fill(`${who} approved it`, {}) : prefix(`${who} approved `, phrases.ing);
    }
    return { phrase, tone: denied ? 'text-danger-text' : 'text-accent-text', icon: denied ? 'close' : 'check' };
  });
</script>

<div class="text-small text-muted">
  <p class="flex items-start gap-2 py-1">
    <span class="flex h-[1.125rem] w-4 shrink-0 items-center justify-center {line.tone}"><Icon name={line.icon} /></span>
    <!-- "· {time} · Show" stays together, so Show is never alone on a line and no line ends in "·". -->
    <span class="min-w-0 [overflow-wrap:anywhere]">
      <PhraseText phrase={line.phrase} />{' '}<span class="whitespace-nowrap"
        >{#if approval.decided_at}· <time datetime={approval.decided_at} title={fullTime(approval.decided_at)}>{clock(approval.decided_at)}</time>{' '}{/if}· <LinkButton
          aria-expanded={show}
          onclick={() => (show = !show)}>{show ? 'Hide' : 'Show'}</LinkButton
        ></span
      >
    </span>
  </p>
  <!-- An approved deploy says how it goes, live, until its outcome notice arrives (§8.7). -->
  {#if approval.status === 'allowed' && approval.action?.tool === 'release_deploy'}<div class="pl-6"><DeployFollowUp {approval} small /></div>{/if}
  <!-- The line above already says the decision, so the card leaves its own out. -->
  {#if show}<div class="mt-2 mb-1"><InlineApproval {approval} readonly decision={false} /></div>{/if}
</div>
