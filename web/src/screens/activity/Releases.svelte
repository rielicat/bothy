<script lang="ts">
  import { untrack } from 'svelte';
  import Icon from '../../components/Icon.svelte';
  import Verbatim from '../../components/Verbatim.svelte';
  import { EmptyState, Spinner } from '../../components/ui';
  import { api } from '../../lib/api/client';
  import type { List } from '../../lib/api/types.gen';
  import { withdrawn } from '../../lib/cardcopy';
  import { date, duration, fullTime, relTime } from '../../lib/format';
  import { router } from '../../lib/router.svelte';
  import { session } from '../../lib/stores/session.svelte';
  import { toneClass, type Tone } from '../../lib/taskStatus';
  import Activity from './Activity.svelte';
  import { loadReleases, people, personName, votesOf, type Deploy, type Releases, type VotedApproval } from './api';
  import Loading from '../../components/ui/Loading.svelte';

  let data = $state<Releases | null>(null);
  let pending = $state<VotedApproval[]>([]);
  let denied = $state<VotedApproval[]>([]);
  let names = $state(new Map<string, string>());
  let failed = $state(false);

  const isDeploy = (a: VotedApproval | undefined): a is VotedApproval => a?.action?.tool === 'release_deploy';
  const envOf = (a: VotedApproval) => String((a.action?.payload as Record<string, unknown> | undefined)?.environment ?? '');
  const open = (d: Deploy) => d.status === 'dispatched' || d.status === 'running';

  async function load() {
    failed = false;
    try {
      const [r, p, recent, n] = await Promise.all([
        loadReleases(true),
        api.get<List<VotedApproval>>('/api/approvals?status=pending').catch(() => ({ items: [] as VotedApproval[] })),
        api.get<List<VotedApproval>>('/api/approvals?status=recent').catch(() => ({ items: [] as VotedApproval[] })),
        people(),
      ]);
      pending = (p.items ?? []).filter(isDeploy);
      // Requests that ended without a deploy: denied, cancelled (withdrawn or stopped) or expired.
      denied = (recent.items ?? []).filter((a) => isDeploy(a) && ['denied', 'cancelled', 'expired'].includes(a.status));
      names = n;
      data = r;
    } catch {
      failed = true;
    }
  }

  // Until deploy events land (S17), an open deploy is checked every 15 s.
  $effect(() => {
    untrack(() => void load());
  });
  $effect(() => {
    if (!data?.deploys.some((d) => d && open(d)) && !data?.environments.some((e) => e.running?.length)) return;
    const t = setTimeout(load, 15_000);
    return () => clearTimeout(t);
  });

  // The viewer's own deploys and those whose approval they may see (an admin's: everyone's), and what each environment runs whoever asked.
  const deploys = $derived(
    [...new Map([...(data?.deploys ?? []), ...(data?.environments ?? []).flatMap((e) => [...(e.running ?? []), e.last])].filter((d): d is Deploy => !!d).map((d) => [d.id, d])).values()].sort(
      (a, b) => b.created_at.localeCompare(a.created_at),
    ),
  );
  const and = (list: string[]) => new Intl.ListFormat('en', { type: 'conjunction' }).format(list);
  const approvers = (d: Deploy) => and(d.approved_by_names);
  const took = (d: Deploy) => {
    const s = date(d.created_at)?.getTime();
    const f = date(d.finished_at)?.getTime();
    return s && f && f > s ? `took ${duration((f - s) * 1e6)}` : '';
  };

  type Status = { text: string; tone: Tone; spin?: boolean; href?: string };
  // unknown: Bothy stopped following the run, so how it ended is on GitHub, not here.
  const deployStatus = (d: Deploy): Status =>
    open(d)
      ? { text: 'Deploying…', tone: 'muted', spin: true }
      : d.status === 'succeeded'
        ? { text: 'Deployed', tone: 'muted' }
        : d.status === 'unknown'
          ? { text: 'Not tracked', tone: 'muted' }
          : { text: 'Failed', tone: 'danger' };
  const shortDay = (t: string) => new Date(t).toLocaleDateString(undefined, { month: 'short', day: 'numeric' });
  const github = (d: Deploy) =>
    d.run_url ? { href: d.run_url, label: d.status === 'failed' ? 'View logs on GitHub' : d.status === 'unknown' ? 'Check on GitHub' : 'View on GitHub' } : undefined;

  const environments = $derived(
    (data?.environments ?? []).map((env) => {
      // The server says what the environment does, whoever deployed; older servers only list the viewer's own deploys.
      const last = env.last ?? deploys.find((d) => d.environment === env.name);
      const running = env.running ?? (last && open(last) ? [last] : []);
      const wait = pending.find((a) => envOf(a) === env.name);
      // A request its asker cancelled says nothing about the environment, so it shows the last deploy.
      const no = denied.find((a) => a.status === 'denied' && envOf(a) === env.name && !withdrawn(a) && (!last || a.created_at > last.created_at));
      let status: Status = { text: 'Not deployed yet', tone: 'muted' };
      if (running.length) status = { ...deployStatus(running[0]), text: running.length > 1 ? `${running.length} deploys running` : 'Deploying…' };
      else if (wait)
        status = { text: `Waiting for approval${votesOf(wait) ? ` (${votesOf(wait)!.approved} of ${votesOf(wait)!.required})` : ''}`, tone: 'warning', href: `/approvals/${encodeURIComponent(wait.id)}` };
      // Not "Denied": beside "Last deployed …" that reads as if the environment itself were refused.
      else if (no) status = { text: 'Last request denied', tone: 'muted' };
      else if (last) status = deployStatus(last);
      // A waiting or denied request is the status, so line 2 says it's about the last deploy, not that request.
      return { env, last, shown: running[0]?.id, status, before: !!last && !open(last) && (!!wait || !!no) };
    }),
  );

  type Item = { key: string; at: string; title: string; href?: string; status: Status; facts: string[]; link?: { href: string; label: string } };
  const me = $derived(session.me?.user.id);
  const history = $derived<Item[]>(
    [
      // The running deploy shown as the environment's status joins the history once it ends; others running beside it are listed here.
      // Another person's deploy comes without its chat or task, which the viewer couldn't open.
      ...deploys.filter((d) => !environments.some((e) => e.shown === d.id)).map((d) => ({
        key: d.id,
        at: d.created_at,
        title: `Deploy ${d.repo} to ${d.environment} · ${d.ref}${d.short_sha ? ` at ${d.short_sha}` : ''}`,
        href: d.run_id ? `/activity/tasks/${encodeURIComponent(d.run_id)}` : d.session_id ? `/chat/${encodeURIComponent(d.session_id)}` : undefined,
        status: deployStatus(d),
        facts: [
          `${personName(d.requested_by, names, d.requested_by_name)} asked`,
          d.approved_by_names.length ? `${approvers(d)} approved` : '',
          // A deploy Bothy stopped following has no end it saw, so no "took".
          d.status === 'failed' || d.status === 'unknown' ? '' : took(d),
        ].filter(Boolean),
        link: github(d),
      })),
      ...denied.map((a) => {
        const p = (a.action?.payload ?? {}) as Record<string, unknown>;
        const reason = a.comment ? `: “${a.comment}”` : '';
        const asker = personName(a.approver?.requester, names, a.requested_by_name);
        const cancelled = withdrawn(a) || a.status === 'cancelled';
        const v = votesOf(a);
        return {
          key: a.id,
          at: a.created_at,
          title: `Deploy ${String(p.repo ?? '')} to ${envOf(a)} · ${String(p.ref ?? '')}`,
          href: a.approver?.requester === me ? `/activity/tasks/${encodeURIComponent(a.run_id)}` : undefined,
          status: { text: a.status === 'expired' ? 'Expired' : cancelled ? 'Cancelled' : 'Denied', tone: 'muted' as Tone },
          facts: [
            `${asker} asked`,
            a.status === 'expired'
              ? v?.approved
                ? `only ${v.approved} of ${v.required} approvals came in time`
                : 'nobody answered in time'
              : a.status === 'cancelled'
                ? 'stopped before it was decided'
                : cancelled
                  ? `Cancelled by ${asker === 'You' ? 'you' : asker}${reason}`
                  : `${personName(a.decided_by, names, a.decided_by_name)} denied${reason}`,
          ],
        };
      }),
    ].sort((a, b) => b.at.localeCompare(a.at)),
  );
</script>

{#snippet setUp()}
  {#if session.isAdmin && data?.environments.length}
    <a href="/settings/connections/github" onclick={router.link} class="inline-flex min-h-6 items-center gap-1 text-body text-muted hover:text-text pointer-coarse:min-h-11"
      >Set up environments<Icon name="chevron-right" /></a
    >
  {/if}
{/snippet}

<!-- The commit subject is written by whoever can push to the repository: quoted, shown as typed, never as markup. -->
{#snippet version(d: Deploy)}{d.ref}{#if d.short_sha}{' · '}<span class="tabular-nums">{d.short_sha}</span>{/if}{#if d.subject}{' · '}<q data-copy-ignore data-subject><Verbatim text={d.subject} /></q>{/if}{/snippet}

{#snippet word(s: Status, cls = '')}
  {#if s.href}
    <a
      href={s.href}
      onclick={router.link}
      class="-my-0.5 inline-flex min-h-6 shrink-0 items-center gap-0.5 text-small hover:underline pointer-coarse:-my-3 pointer-coarse:min-h-11 {toneClass[s.tone] || 'text-muted'} {cls}"
      >{s.text}<Icon name="chevron-right" size={12} /></a
    >
  {:else}
    <span class="inline-flex shrink-0 items-center gap-1.5 text-small {toneClass[s.tone] || 'text-muted'} {cls}">{#if s.spin}<Spinner size={12} />{/if}{s.text}</span>
  {/if}
{/snippet}

<!-- The label and ↗ stay together, so the arrow is never alone on a line. -->
{#snippet external(l: { href: string; label: string }, cls = '')}
  <a class="link whitespace-nowrap {cls}" href={l.href} target="_blank" rel="noopener noreferrer">{l.label} ↗<span class="sr-only"> (opens in a new tab)</span></a>
{/snippet}

<Activity tab="Releases" actions={setUp}>
  {#if !data}
    <Loading {failed} retry={load} />
  {:else if !data.environments.length}
    <EmptyState
      text="No deploy environments yet. Add one so Bothy can deploy with approval."
      action={session.isAdmin ? { label: 'Set up environments', href: '/settings/connections/github' } : undefined}
    />
  {:else if !history.length && !pending.length && !deploys.length}
    <EmptyState text="No deploys yet. Ask Bothy in a chat, for example “Deploy main to staging”." />
  {:else}
    <h2 class="section-title">Environments</h2>
    <ul class="list-card mt-2">
      {#each environments as { env, last, status, before } (env.name)}
        <li class="border-b border-border py-2 last:border-b-0">
          <!-- Not a link to anything, so the name wraps instead of truncating; a long status moves under it. -->
          <div class="flex min-h-6 flex-wrap items-baseline justify-between gap-x-4">
            <span class="min-w-0 text-body font-medium text-text [overflow-wrap:anywhere]">{env.name} · {env.repo}</span>
            {@render word(status)}
          </div>
          {#if last}
            <p class="text-small text-muted [overflow-wrap:anywhere]">
              {#if before}Last deployed <time title={fullTime(last.created_at)}>{shortDay(last.created_at)}</time> ·{' '}{/if}{@render version(last)}{#if before}{#if last.approved_by_names.length}{` · approved by ${approvers(last)}`}{/if}{:else}{' · '}{#if open(last)}started {relTime(last.created_at)}{:else}<time title={fullTime(last.created_at)}>{relTime(last.created_at)}</time>{/if}{#if !open(last) && last.approved_by_names.length}{` · approved by ${approvers(last)}`}{/if}{#if (open(last) || last.status === 'unknown') && github(last)}{' · '}{@render external(github(last)!)}{/if}{/if}
            </p>
          {/if}
        </li>
      {/each}
    </ul>
    <p class="mt-4 text-small text-muted">To deploy, ask Bothy in a chat, for example “Deploy {data.environments[0].repo} to {data.environments[data.environments.length - 1].name}”.</p>

    {#if history.length}
      <section class="mt-10">
        <h2 class="section-title">History</h2>
        <ul class="list-card mt-2 overflow-hidden">
          {#each history as h (h.key)}
            <!-- A row that opens its task is a link stretched over it; the GitHub link sits above it. -->
            <li class="flex min-h-14 items-center gap-3 border-b border-border py-2 last:border-b-0 {h.href ? 'row-stretch' : ''}">
              <div class="min-w-0 flex-1">
                <!-- On a phone the title wraps, since rows differ at its end (environment, commit), and the status leads the meta line. -->
                <div class="flex items-baseline gap-4">
                  {#if h.href}
                    <a
                      href={h.href}
                      onclick={router.link}
                      class="stretch-link -my-0.5 flex min-w-0 flex-1 items-center py-0.5 text-body font-medium text-text pointer-coarse:-my-3 pointer-coarse:py-3"
                      title={h.title}><span class="[overflow-wrap:anywhere] sm:truncate">{h.title}</span></a
                    >
                  {:else}
                    <span class="flex min-h-6 min-w-0 flex-1 items-center text-body font-medium text-text" title={h.title}><span class="[overflow-wrap:anywhere] sm:truncate">{h.title}</span></span>
                  {/if}
                  {@render word(h.status, 'max-sm:hidden')}
                  <time class="meta min-w-20 text-right whitespace-nowrap max-sm:hidden" title={fullTime(h.at)}>{relTime(h.at)}</time>
                </div>
                <p class="text-small text-muted [overflow-wrap:anywhere]">
                  <span class="sm:hidden">{@render word(h.status)}{' · '}</span>{h.facts.join(' · ')}{#if h.link}{' · '}{@render external(h.link, 'relative z-10')}{/if}<span
                    class="sm:hidden">{' · '}<span class="whitespace-nowrap">{relTime(h.at)}</span></span
                  >
                </p>
              </div>
              <!-- Room for the › on rows that don't open anything, so the times line up. -->
              {#if h.href}<Icon name="chevron-right" class="shrink-0 text-muted" />{:else}<span class="w-4 shrink-0" aria-hidden="true"></span>{/if}
            </li>
          {/each}
        </ul>
      </section>
    {/if}
  {/if}
</Activity>
