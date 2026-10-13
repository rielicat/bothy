<script lang="ts">
  import { api } from '../lib/api/client';
  import type { Approval, Choice } from '../lib/api/types.gen';
  import { countdown, ago, isZeroTime } from '../lib/format';
  import { channelName, choiceDone, choiceKey, choiceLabel, risk } from '../lib/labels';
  import { clock } from '../lib/stores/clock.svelte';
  import { approvals, session } from '../lib/stores/session.svelte';
  import { toasts } from '../lib/stores/ui.svelte';
  import { splitUrl } from '../lib/text';
  import DiffView from './DiffView.svelte';
  import Icon from './Icon.svelte';
  import Verbatim from './Verbatim.svelte';

  let {
    approval,
    reason = '',
    compact = false,
    shortcuts = false,
    preview = false,
    onresolved,
  }: {
    approval: Approval;
    // Free text from the model, shown collapsed and never as markup; no DTO
    // field carries it today, so only callers that have it pass it.
    reason?: string;
    compact?: boolean;
    shortcuts?: boolean;
    // Policy tester: render the card without actions.
    preview?: boolean;
    onresolved?: (a: Approval) => void;
  } = $props();

  const id = $props.id();
  let busy = $state<Choice | ''>('');
  let resolved = $state<Approval | null>(null);

  const view = $derived(resolved?.id === approval.id ? resolved : approval);
  const card = $derived(view.card);
  const open = $derived(view.status === 'pending' || view.status === 'parked');
  const tone = $derived(risk[card.risk] ?? risk.external_send);
  const me = $derived(session.me?.user);
  // Fails closed: an approval that names no approver shows no buttons.
  const canApprove = $derived(!!me && (view.approver?.admin_only ? session.isAdmin : !!view.approver?.user_id && view.approver.user_id === me.id));
  // Go sends unset times as 0001-01-01, which is truthy.
  const deadline = $derived(view.status === 'parked' ? view.park_until : isZeroTime(card.expires) ? view.expires_at : card.expires);
  const left = $derived(open && deadline ? countdown(deadline, clock.now) : '');
  // The rendered card is authoritative: a choice it leaves out is never offered.
  const choices = $derived(card.choices ?? []);
  const order: Choice[] = ['allow_once', 'allow_session', 'allow_always', 'deny'];
  const buttons = $derived(order.filter((c) => choices.includes(c)));

  const outcome = $derived.by(() => {
    const who = view.decided_by ? (view.decided_by === me?.id ? 'you' : view.decided_by) : '';
    const via = view.decided_via ? channelName[view.decided_via] ?? view.decided_via : '';
    const what =
      view.status === 'allowed'
        ? choiceDone[view.choice ?? 'allow_once']
        : view.status === 'denied'
          ? 'Denied'
          : view.status === 'expired'
            ? `Expired: ${view.on_timeout === 'park' ? 'parked' : 'denied'}`
            : view.status === 'cancelled'
              ? 'Cancelled'
              : '';
    return [what, who && `by ${who}`, via && `via ${via}`, view.decided_at && ago(view.decided_at, clock.now)].filter(Boolean).join(' · ');
  });

  async function resolve(choice: Choice) {
    if (!open || !canApprove || busy || !buttons.includes(choice)) return;
    busy = choice;
    try {
      const a = await api.post<Approval>(`/api/approvals/${encodeURIComponent(view.id)}`, { choice });
      resolved = a;
      approvals.upsert(a);
      onresolved?.(a);
    } catch (e) {
      toasts.error(e);
    } finally {
      busy = '';
    }
  }
</script>

<article
  class="card overflow-hidden {compact ? 'text-sm' : ''} {open ? 'border-warn/50 shadow-sm' : ''}"
  aria-labelledby="{id}-t"
  data-approval={view.id}
>
  <header class="flex flex-wrap items-center gap-2 border-b border-line px-4 py-3">
    <span class="inline-flex items-center gap-1 rounded-full border px-2 py-0.5 text-xs font-medium {tone.cls}">
      <Icon name="shield" size={13} />{tone.label}
    </span>
    <h2 id="{id}-t" class="min-w-0 flex-1 font-semibold"><Verbatim text={card.title} /></h2>
    {#if open && left}
      <span class="chip chip-warn tabular-nums" title={view.status === 'parked' ? 'Parked until' : 'Expires in'}>
        <Icon name="clock" size={12} />{view.status === 'parked' ? 'parked · ' : ''}{left}
      </span>
    {/if}
  </header>

  <div class="space-y-3 px-4 py-3">
    {#if card.fields?.length}
      <dl class="grid gap-x-4 gap-y-2 sm:grid-cols-[max-content_1fr]">
        {#each card.fields as f, i (i)}
          <dt class="text-xs font-medium tracking-wide text-muted uppercase sm:pt-1">{f.label}</dt>
          <dd class="min-w-0">
            {#if f.mono}
              {@const u = splitUrl(f.value)}
              <div class="mono rounded-md bg-sunken px-2 py-1 break-all whitespace-pre-wrap">
                {#if u}<Verbatim text={u.before} /><strong class="text-fg"><Verbatim text={u.host} /></strong><Verbatim
                    text={u.after}
                  />{:else}<Verbatim text={f.value} />{/if}
              </div>
            {:else}
              <span class="break-words whitespace-pre-wrap"><Verbatim text={f.value} /></span>
            {/if}
          </dd>
        {/each}
      </dl>
    {/if}

    {#if card.code}
      <pre class="mono max-h-72 overflow-auto rounded-lg border border-line bg-sunken p-3 whitespace-pre-wrap break-all"><code
          ><Verbatim text={card.code} /></code
        ></pre>
    {/if}

    {#if card.diff}
      <DiffView diff={card.diff} />
    {/if}

    {#if card.warnings?.length}
      <ul class="space-y-1.5">
        {#each card.warnings as w, i (i)}
          <li class="flex gap-2 rounded-lg bg-warn-soft px-3 py-2 text-sm text-warn">
            <Icon name="alert" size={16} class="mt-0.5" /><span><Verbatim text={w} /></span>
          </li>
        {/each}
      </ul>
    {/if}

    {#if card.context}
      <p class="text-xs text-muted"><Verbatim text={card.context} /></p>
    {/if}

    {#if reason}
      <details class="rounded-lg border border-line px-3 py-2 text-sm">
        <summary class="cursor-pointer text-muted">Model's reason (unverified)</summary>
        <p class="mt-2 whitespace-pre-wrap"><Verbatim text={reason} /></p>
      </details>
    {/if}
  </div>

  {#if !preview}
  <footer class="flex flex-wrap items-center gap-2 border-t border-line bg-sunken/50 px-4 py-3">
    {#if open && canApprove}
      {#each buttons as c (c)}
        <button
          type="button"
          class="btn {c === 'deny' ? 'btn-danger' : c === 'allow_once' ? 'btn-primary' : ''} {compact ? 'btn-sm' : ''}"
          disabled={!!busy}
          data-choice={c}
          aria-keyshortcuts={shortcuts ? choiceKey[c] : undefined}
          onclick={() => resolve(c)}
        >
          {choiceLabel[c]}
          {#if shortcuts && choiceKey[c]}<kbd class="kbd ml-1">{choiceKey[c]}</kbd>{/if}
        </button>
      {/each}
    {:else if open}
      <p class="text-sm text-muted" role="status">
        Waiting for {view.approver?.admin_only ? 'an admin' : 'the owner'} to decide.
      </p>
    {:else}
      <p class="flex items-center gap-1.5 text-sm {view.status === 'allowed' ? 'text-ok' : 'text-muted'}" role="status">
        <Icon name={view.status === 'allowed' ? 'check' : 'x'} size={15} />{outcome}
      </p>
    {/if}
  </footer>
  {/if}
</article>
