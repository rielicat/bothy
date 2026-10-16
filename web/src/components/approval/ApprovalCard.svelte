<script lang="ts">
  import { tick } from 'svelte';
  import { api, ApiError } from '../../lib/api/client';
  import type { Approval, Choice } from '../../lib/api/types.gen';
  import { appName, busyLabel, cardCopy, decidable, expiryOf, fromTeam, isMulti, isOpen, originOf, requesterOf, teamAsked, teamPerson } from '../../lib/cardcopy';
  import { choiceDone, choiceKey } from '../../lib/labels';
  import { router } from '../../lib/router.svelte';
  import { clock } from '../../lib/stores/clock.svelte';
  import { approvals, session } from '../../lib/stores/session.svelte';
  import Icon from '../Icon.svelte';
  import { announce } from '../ui/live.svelte';
  import { tooltip } from '../ui/tooltip';
  import Button from '../ui/Button.svelte';
  import CodeBlock from '../ui/CodeBlock.svelte';
  import Disclosure from '../ui/Disclosure.svelte';
  import IconButton from '../ui/IconButton.svelte';
  import LinkButton from '../ui/LinkButton.svelte';
  import Notice from '../ui/Notice.svelte';
  import TextField from '../ui/TextField.svelte';
  import Verbatim from '../Verbatim.svelte';
  import AlwaysAllowDialog from './AlwaysAllowDialog.svelte';
  import ApprovalFields from './ApprovalFields.svelte';
  import ApprovalVotes from './ApprovalVotes.svelte';
  import Self from './ApprovalCard.svelte';
  import DecidedLine from './DecidedLine.svelte';
  import { people } from './people.svelte';
  import type { Viewer } from './types';
  import { votes } from './votes.svelte';
  import Words from './Words.svelte';

  // The approval card (§8.1–8.5), rendered only from the server's card and
  // the structured Action, never from model prose.
  let {
    approval,
    mode = 'inline',
    viewer,
    reason = '',
    level = 2,
    onresolve,
    onresolved,
    class: cls = '',
  }: {
    approval: Approval;
    /**
     * inline: in a chat; collapses to its decided line once decided.
     * inbox: the expanded inbox row; the inbox owns A/S/D and focus.
     * decided: the decided line with "Show". readonly: the card without buttons.
     */
    mode?: 'inline' | 'inbox' | 'decided' | 'readonly';
    viewer?: Viewer;
    /** Bothy's own explanation, when the caller has it; shown collapsed and as plain text. */
    reason?: string;
    /** Heading level of the question. */
    level?: 2 | 3;
    /** After this card sent a decision: the choice, the deny reason and the updated approval. */
    onresolve?: (choice: Choice, comment: string | undefined, result: Approval) => void;
    onresolved?: (a: Approval) => void;
    class?: string;
  } = $props();

  const uid = $props.id();
  let resolved = $state<Approval | null>(null);
  let busy: Choice | '' = $state('');
  let failed = $state('');
  let denying = $state(false);
  let comment = $state('');
  let reasonInput: HTMLInputElement | undefined = $state();
  let alwaysOpen = $state(false);
  let detailsOpen = $state(false);
  let showCard = $state(false);
  let article: HTMLElement | undefined = $state();
  let decidedLine: DecidedLine | undefined = $state();
  // A held shortcut key opens the reason field; its repeats must not type into it.
  let swallowRepeat = false;

  // An event can bring a newer copy than this card's own result, but a stale prop never undoes a decision.
  const view = $derived((resolved && resolved.id === approval.id && isOpen(approval) ? resolved : approval));
  const copy = $derived(cardCopy(view));
  const open = $derived(isOpen(view));
  const multi = $derived(isMulti(view));
  const me = $derived(viewer ?? (session.me ? { id: session.me.user.id, role: session.me.user.role } : undefined));
  const vv = $derived(multi ? votes.byId[view.id] : undefined);
  const myVote = $derived(vv?.your_vote ?? view.votes?.find((v) => v.user_id === me?.id)?.choice);
  const requesterId = $derived(requesterOf(view, vv));
  const isRequester = $derived(multi && !!me && requesterId === me.id);
  const canDecide = $derived(decidable(view, me, vv));

  const offered = $derived(view.card?.choices ?? []);
  const primaryChoice = $derived<Choice | null>(offered.includes('allow_once') ? 'allow_once' : offered.includes('allow_session') ? 'allow_session' : null);
  // The rendered card is authoritative: a choice it leaves out is never offered.
  const buttons = $derived((['allow_once', 'allow_session', 'deny'] as Choice[]).filter((c) => offered.includes(c) && !(multi && c === 'allow_session')));
  const always = $derived(offered.includes('allow_always') && copy.tone !== 'destructive' && !multi);
  const keys = $derived(mode === 'inbox');

  // A teammate's request names their chat: the approver may be reading it in Approvals, not there (§8.4).
  const person = $derived(teamPerson(view));
  const label = (c: Choice) => (c === 'deny' ? 'Deny' : c === 'allow_session' ? (person ? `Allow in ${person}'s chat` : 'Allow for this chat') : copy.primary);
  const variant = (c: Choice) => (c !== primaryChoice ? 'secondary' : c === 'allow_once' && copy.tone === 'destructive' ? 'danger' : 'primary');
  const keyHint = (c: Choice) => (keys && choiceKey[c] ? `${label(c)} (${choiceKey[c]!.toUpperCase()})` : undefined);

  const origin = $derived(originOf(view));
  const inOwnChat = $derived(!!view.session_id && router.path === `/chat/${encodeURIComponent(view.session_id)}`);
  const team = $derived(fromTeam(view));
  const someoneElse = $derived(team || (!!requesterId && !!me && requesterId !== me.id));
  const requesterName = $derived((team && view.requester?.name) || (multi && vv?.requester_name) || people.name(requesterId) || 'Someone');
  const expiry = $derived(expiryOf(view, clock.now));
  const warnings = $derived(copy.warnings.slice(0, 2));
  const moreWarnings = $derived(copy.warnings.slice(2));
  const whoCan = $derived(
    multi
      ? vv?.approvers || 'the approvers this environment names'
      : view.approver?.admin_only
        ? 'any admin'
        : view.approver?.team
          ? 'an admin, or someone who can approve in a chat app'
          : people.name(view.approver?.user_id) || 'the person who asked',
  );
  const denyHint = $derived(!team ? undefined : view.requester?.chat ? `People in ${view.requester.chat} won't see this, but Bothy will.` : `${requesterName} will see this.`);
  // A schedule's request was asked for by nobody in the moment, so it says where it came from instead.
  // Someone else's request names the person and the chat once, here, so the meta line below leaves them out (§8.5).
  const askedIn = $derived(!team && someoneElse && !origin.schedule && !!origin.app && !!origin.title);
  const asked = $derived(
    team
      ? teamAsked(view)
      : origin.schedule
      ? `Started by a schedule${origin.app ? ` on ${origin.app}` : ''}.`
      : origin.app
        ? `${someoneElse ? requesterName : 'You'} asked ${origin.group ? 'in a group chat on' : 'on'} ${origin.app}.`
        : '',
  );
  const left = $derived(vv ? Math.max(0, vv.required - vv.approvals) : 1);
  const technical = $derived<[string, string | undefined][]>([
    ['Tool', view.action?.tool],
    ['Rule', view.decision?.rule],
    ['Request', view.id],
  ]);

  $effect(() => {
    if (!multi || !open) return;
    const id = view.id;
    void votes.load(id);
    const t = setInterval(() => votes.load(id), 15_000);
    return () => clearInterval(t);
  });

  async function resolve(choice: Choice, text?: string) {
    if (!open || busy) return;
    if (!canDecide && !(isRequester && choice === 'deny')) return;
    busy = choice;
    failed = '';
    try {
      const note = text?.trim() || undefined;
      const a = await api.post<Approval>(`/api/approvals/${encodeURIComponent(view.id)}`, note ? { choice, comment: note } : { choice });
      resolved = a;
      denying = false;
      alwaysOpen = false;
      approvals.upsert(a);
      if (multi) void votes.load(a.id);
      onresolve?.(choice, note, a);
      onresolved?.(a);
      if (mode === 'inline') {
        announce(`${choiceDone[choice]}.`);
        await tick();
        // The pressed button is gone; focus goes to the line that replaced it.
        if (!document.activeElement || document.activeElement === document.body) (decidedLine ?? article)?.focus();
      }
    } catch (e) {
      failed = await refusal(e);
    } finally {
      busy = '';
    }
  }

  // A refusal the server explains (decided, already voted, asked by you) won't
  // go away by trying again: the card says why and shows where the request stands.
  async function refusal(e: unknown): Promise<string> {
    const retry = "Couldn't send your decision. Try again.";
    if (!(e instanceof ApiError) || (e.status !== 403 && e.status !== 409)) return retry;
    const id = view.id;
    const fresh = await api.get<Approval>(`/api/approvals/${encodeURIComponent(id)}`).catch(() => null);
    if (multi) void votes.load(id);
    if (fresh) {
      resolved = fresh;
      approvals.upsert(fresh);
      if (!isOpen(fresh)) {
        onresolved?.(fresh);
        // An inline card folds into its decided line, which has no room for the notice.
        if (mode === 'inline') announce('This request was already decided.');
        return 'This request was already decided.';
      }
    }
    const why = e.message.trim();
    if (!why) return retry;
    return `${why[0].toUpperCase()}${why.slice(1)}${/[.?]$/.test(why) ? '' : '.'}`;
  }

  async function startDeny() {
    if (busy) return;
    denying = true;
    swallowRepeat = true;
    await tick();
    reasonInput?.focus();
  }

  async function cancelDeny() {
    denying = false;
    comment = '';
    await tick();
    article?.querySelector<HTMLElement>('button[data-choice="deny"]')?.focus();
  }

  function choose(c: Choice) {
    if (c === 'deny') void startDeny();
    else void resolve(c);
  }

  // In a chat the shortcuts work only while the card has focus (§16); the inbox drives its own.
  function onkeydown(e: KeyboardEvent) {
    if (mode !== 'inline' || e.metaKey || e.ctrlKey || e.altKey || e.defaultPrevented || e.repeat) return;
    if (e.target instanceof Element && e.target.closest('input, textarea, select, [contenteditable]')) return;
    const c = ({ a: 'allow_once', s: 'allow_session', d: 'deny' } as Record<string, Choice>)[e.key.toLowerCase()];
    const b = c && article?.querySelector<HTMLButtonElement>(`button[data-choice="${c}"]`);
    if (!b) return;
    e.preventDefault();
    b.click();
  }

  async function copyText(text: string) {
    try {
      await navigator.clipboard.writeText(text);
      announce('Copied');
    } catch {
      // no clipboard (plain http): the value is on screen to select
    }
  }
</script>

{#if mode === 'decided' || (mode === 'inline' && !open)}
  <div class={cls}>
    <DecidedLine bind:this={decidedLine} approval={view} {viewer} expanded={showCard} onshow={() => (showCard = !showCard)} />
    {#if showCard}
      <div class="mt-2"><Self approval={view} mode="readonly" {viewer} {reason} {level} /></div>
    {/if}
  </div>
{:else}
  <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
  <article
    bind:this={article}
    class="card focus-visible:outline-offset-[-1px] {mode === 'inbox' ? 'sm:p-5' : ''} {cls}"
    aria-labelledby="{uid}-q"
    data-approval={view.id}
    tabindex="-1"
    {onkeydown}
  >
    <svelte:element this={level === 3 ? 'h3' : 'h2'} id="{uid}-q" class="text-heading text-text [overflow-wrap:anywhere]"><Words segs={copy.question} /></svelte:element>
    {#if copy.consequence}<p class="mt-1 text-body text-muted">{copy.consequence}</p>{/if}
    {#if asked}
      <p class="text-body text-muted {copy.consequence ? '' : 'mt-1'}">
        {#if askedIn}{requesterName} asked in <span data-copy-ignore>“<Verbatim text={origin.title ?? ''} />”</span> on {origin.app}.{:else}{asked}{/if}
      </p>
    {/if}

    {#if warnings.length}
      <ul class="mt-3 space-y-1">
        {#each warnings as w (w)}
          <li class="flex items-start gap-1.5 text-small text-warning-text">
            <span class="flex h-[1.125rem] w-4 shrink-0 items-center justify-center"><Icon name="alert" /></span><span class="min-w-0"><Verbatim text={w} /></span>
          </li>
        {/each}
      </ul>
    {/if}

    {#if copy.fields.length}
      <div class="mt-4">
        <ApprovalFields fields={copy.fields} showOpen={mode === 'inbox' && copy.kind === 'pull_request'} votes={multi && vv ? votesRow : undefined} />
      </div>
    {/if}

    {#if mode !== 'readonly'}
      {#if failed}
        <Notice class="mt-4" tone="danger" text={failed} role="alert" />
      {/if}

      {#if open && canDecide && denying}
        <form
          class="mt-4"
          onsubmit={(e) => {
            e.preventDefault();
            void resolve('deny', comment);
          }}
        >
          <TextField
            label="Reason"
            optional
            placeholder={denyHint}
            bind:value={comment}
            bind:input={reasonInput}
            autocomplete="off"
            onkeydown={(e: KeyboardEvent) => {
              if (e.key === 'Escape') {
                e.preventDefault();
                e.stopPropagation();
                void cancelDeny();
              } else if (e.repeat && swallowRepeat) e.preventDefault();
            }}
            onkeyup={() => (swallowRepeat = false)}
          />
          <div class="mt-3 flex flex-wrap gap-2 max-[30rem]:flex-col max-[30rem]:[&>.btn]:w-full">
            <Button type="submit" loading={busy === 'deny'} loadingLabel={busyLabel('Deny')} data-choice="deny-confirm">Deny</Button>
            <Button variant="ghost" disabled={!!busy} onclick={cancelDeny}>Cancel</Button>
          </div>
        </form>
      {:else if open && canDecide}
        <div class="mt-4 flex flex-wrap gap-2 max-[30rem]:flex-col max-[30rem]:[&>.btn]:w-full">
          {#each buttons as c (c)}
            <Button
              variant={variant(c)}
              loading={busy === c}
              loadingLabel={busyLabel(label(c))}
              disabled={!!busy && busy !== c}
              data-choice={c}
              aria-keyshortcuts={keys ? choiceKey[c]?.toUpperCase() : undefined}
              onclick={() => choose(c)}
              {@attach keys ? tooltip(() => keyHint(c)) : undefined}>{label(c)}</Button
            >
          {/each}
        </div>
        {#if always}
          <p class="mt-2 text-small">
            <LinkButton class="text-left" disabled={!!busy} onclick={() => (alwaysOpen = true)}
              >{#if person}Always allow for {person}…{:else}<span>Always allow <Words segs={copy.grant} />…</span>{/if}</LinkButton
            >
          </p>
        {/if}
      {:else if open}
        <div class="mt-4 text-body text-muted" role="status">
          {#if multi && myVote === 'allow_once'}
            <p>You approved. {left === 1 ? 'Waiting for one more.' : `Waiting for ${left} more.`}</p>
          {:else if isRequester}
            {#if vv?.approvals}
              <p>You asked for this. {vv.approvals} of {vv.required} approvals so far; {left === 1 ? 'one more person needs' : `${left} more people need`} to approve it.</p>
            {:else}
              <p>You asked for this, so {vv?.required ?? view.approver.required ?? 1} other {(vv?.required ?? view.approver.required ?? 1) === 1 ? 'person needs' : 'people need'} to approve it.</p>
            {/if}
            <Button class="mt-3" loading={busy === 'deny'} loadingLabel="Cancelling…" onclick={() => resolve('deny')}>Cancel request</Button>
          {:else if multi}
            <p>Waiting for {vv?.approvers || 'the other approvers'}.</p>
          {:else}
            <p>Waiting for {view.approver?.admin_only ? 'an admin' : view.approver?.team ? 'an approver' : people.name(view.approver?.user_id) || 'someone else'}.</p>
          {/if}
        </div>
      {/if}
    {:else if !open}
      <div class="mt-4"><DecidedLine approval={view} {viewer} followUp={false} /></div>
    {/if}

    <p class="mt-3 text-small text-muted">
      <!-- Inside its own chat the card needs no "From" (§7.1). -->
      <!-- Someone else's chat title may be private; who asked never is. -->
      <!-- A team request's chat isn't on the web; the server names it instead. -->
      {#if team}
        {#if view.card?.context}<span data-copy-ignore>{view.card.context}</span>{' · '}{/if}
      {:else if !inOwnChat && view.session_id && (!someoneElse || !askedIn)}
        {#if someoneElse}
          Requested by {requesterName}{#if origin.title}{' '}in <span data-copy-ignore>“<Verbatim text={origin.title} />”</span>{/if}
        {:else}
          From
          <a class="link" href="/chat/{encodeURIComponent(view.session_id)}" onclick={router.link}
            >{#if origin.title}<span data-copy-ignore>“<Verbatim text={origin.title} />”</span>{:else}a chat{/if}</a
          >
        {/if}
        {' · '}
      {/if}
      {#if expiry && open}
        <span class="{expiry.urgent ? 'text-warning-text' : ''} {expiry.text.length < 30 ? 'whitespace-nowrap' : ''}">{expiry.text}</span>{' · '}
      {/if}
      <button
        type="button"
        class="-my-0.5 inline-flex min-h-6 cursor-pointer items-center gap-1 text-small text-muted hover:text-text pointer-coarse:-my-3 pointer-coarse:min-h-11"
        aria-expanded={detailsOpen}
        aria-controls="{uid}-details"
        onclick={() => (detailsOpen = !detailsOpen)}
        ><Icon name="chevron-right" size={12} class="transition-transform duration-160 {detailsOpen ? 'rotate-90' : ''}" />Details</button
      >
    </p>
    <div id="{uid}-details" data-details>
      {#if detailsOpen}
        <div class="mt-3 space-y-2 text-small text-muted">
          {#if reason}
            <Disclosure label="Bothy's explanation (written by the AI, not checked)">
              <p class="text-body whitespace-pre-wrap text-text [overflow-wrap:anywhere]"><Verbatim text={reason} /></p>
            </Disclosure>
          {/if}
          {#if view.decision?.reason}<p>Why Bothy asked: <Verbatim text={view.decision.reason} /></p>{/if}
          <p>If nobody answers: {view.on_timeout === 'park' ? 'it will wait' : 'it will be denied'}</p>
          <p>Who can approve: {whoCan}</p>
          {#each moreWarnings as w (w)}<p class="text-warning-text"><Verbatim text={w} /></p>{/each}
          <CodeBlock label="Exact action" code={JSON.stringify(view.action ?? view.card, null, 2)} wrap maxLines={12} />
          <dl class="grid grid-cols-[6rem_minmax(0,1fr)] items-center gap-x-4">
            {#each technical as [k, v] (k)}
              {#if v}
                <dt>{k}</dt>
                <dd class="flex min-w-0 items-center gap-1"><span class="truncate font-mono text-code text-text">{v}</span><IconButton icon="copy" label="Copy {k.toLowerCase()}" size="sm" variant="quiet" onclick={() => copyText(v)} /></dd>
              {/if}
            {/each}
          </dl>
        </div>
      {/if}
    </div>
  </article>
  {#if always}
    <AlwaysAllowDialog
      bind:open={alwaysOpen}
      grant={copy.grant}
      who={person ? `${person} on ${appName(view.requester?.app)}` : ''}
      busy={busy === 'allow_always'}
      onconfirm={() => resolve('allow_always')}
    />
  {/if}
{/if}

{#snippet votesRow()}
  {#if vv}<ApprovalVotes view={vv} {open} />{/if}
{/snippet}
