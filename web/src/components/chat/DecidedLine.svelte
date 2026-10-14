<script lang="ts">
  import { fill, prefix, type Phrase, type Phrases } from '../../lib/activity';
  import type { Approval } from '../../lib/api/types.gen';
  import { clock, fullTime } from '../../lib/format';
  import Icon from '../Icon.svelte';
  import LinkButton from '../ui/LinkButton.svelte';
  import InlineApproval from './InlineApproval.svelte';
  import { appName, people } from './people.svelte';
  import PhraseText from './PhraseText.svelte';

  // The one-line record a decided card leaves in the transcript (§8.4). Local
  // until C4's DecidedLine is exported from components/approval.
  let { approval, phrases }: { approval: Approval; phrases: Phrases } = $props();

  let show = $state(false);

  const line = $derived.by((): { phrase: Phrase; tone: string; icon: 'check' | 'close' } => {
    const a = approval;
    const by = people.name(a.decided_by);
    const via = a.decided_via && !['web', 'timeout', 'stop'].includes(a.decided_via) ? appName[a.decided_via] : '';
    const comment = (a as Approval & { comment?: string }).comment;
    if (a.status === 'expired') return { phrase: prefix("Expired: Bothy didn't ", phrases.ask), tone: 'text-muted', icon: 'close' };
    if (a.status === 'cancelled') return { phrase: fill(a.decided_via === 'stop' ? 'Cancelled when the chat was stopped' : 'Cancelled by the person who asked', {}), tone: 'text-muted', icon: 'close' };
    const denied = a.status === 'denied';
    let phrase: Phrase;
    if (via) phrase = fill(`${by} ${denied ? 'denied' : 'approved'} in ${via}`, {});
    else if (denied) phrase = [...prefix(`${by} denied `, phrases.ing), ...(comment ? fill(' · “{c}”', { c: comment }) : [])];
    else if (a.choice === 'allow_session') phrase = [...prefix(`${by} allowed `, phrases.ing), { k: 'text', v: ' for this chat' }];
    else if (a.choice === 'allow_always') phrase = prefix(`${by} always ${by === 'You' ? 'allow' : 'allows'} `, phrases.ing);
    else phrase = prefix(`${by} approved `, phrases.ing);
    return { phrase, tone: denied ? 'text-danger-text' : 'text-accent-text', icon: denied ? 'close' : 'check' };
  });
</script>

<div class="text-small text-muted">
  <p class="flex items-start gap-2 py-1">
    <span class="flex h-[1.125rem] w-4 shrink-0 items-center justify-center {line.tone}"><Icon name={line.icon} /></span>
    <span class="min-w-0 [overflow-wrap:anywhere]">
      <PhraseText phrase={line.phrase} />{#if approval.decided_at}{' · '}<time datetime={approval.decided_at} title={fullTime(approval.decided_at)}>{clock(approval.decided_at)}</time
        >{/if}{' · '}<LinkButton aria-expanded={show} onclick={() => (show = !show)}>{show ? 'Hide' : 'Show'}</LinkButton>
    </span>
  </p>
  {#if show}<div class="mt-2 mb-1"><InlineApproval {approval} readonly /></div>{/if}
</div>
