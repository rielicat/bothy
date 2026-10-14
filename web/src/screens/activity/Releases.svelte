<script lang="ts">
  import { untrack } from 'svelte';
  import Icon from '../../components/Icon.svelte';
  import { EmptyState, Spinner } from '../../components/ui';
  import { api } from '../../lib/api/client';
  import type { List } from '../../lib/api/types.gen';
  import { date, duration, fullTime, relTime } from '../../lib/format';
  import { router } from '../../lib/router.svelte';
  import { session } from '../../lib/stores/session.svelte';
  import { toneClass, type Tone } from '../../lib/taskStatus';
  import Activity from './Activity.svelte';
  import { loadReleases, people, personName, votesOf, type Deploy, type Releases, type VotedApproval } from './api';
  import Loading from './Loading.svelte';

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
      denied = (recent.items ?? []).filter((a) => isDeploy(a) && a.status === 'denied');
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
    if (!data?.deploys.some((d) => d && open(d))) return;
    const t = setTimeout(load, 15_000);
    return () => clearTimeout(t);
  });

  const deploys = $derived((data?.deploys ?? []).filter((d): d is Deploy => !!d).sort((a, b) => b.created_at.localeCompare(a.created_at)));
  const and = (list: string[]) => new Intl.ListFormat('en', { type: 'conjunction' }).format(list);
  const approvers = (d: Deploy) => and(d.approved_by_names ?? d.approved_by.map((id) => personName(id, names)));
  const version = (d: Deploy) => [d.ref, d.sha?.slice(0, 7), d.subject].filter(Boolean).join(' · ');
  const took = (d: Deploy) => {
    const s = date(d.created_at)?.getTime();
    const f = date(d.finished_at)?.getTime();
    return s && f && f > s ? `took ${duration((f - s) * 1e6)}` : '';
  };

  type Status = { text: string; tone: Tone; spin?: boolean };
  const deployStatus = (d: Deploy): Status =>
    open(d) ? { text: 'Deploying…', tone: 'muted', spin: true } : d.status === 'succeeded' ? { text: 'Deployed', tone: 'muted' } : { text: 'Failed', tone: 'danger' };

  const environments = $derived(
    (data?.environments ?? []).map((env) => {
      const last = deploys.find((d) => d.environment === env.name);
      const wait = pending.find((a) => envOf(a) === env.name);
      const no = denied.find((a) => envOf(a) === env.name && (!last || a.created_at > last.created_at));
      let status: Status = { text: 'Not deployed yet', tone: 'muted' };
      if (last && open(last)) status = deployStatus(last);
      else if (wait) status = { text: `Waiting for approval${votesOf(wait) ? ` (${votesOf(wait)!.approved} of ${votesOf(wait)!.required})` : ''}`, tone: 'warning' };
      else if (no) status = { text: 'Denied', tone: 'muted' };
      else if (last) status = deployStatus(last);
      return { env, last, status };
    }),
  );

  type Item = { key: string; at: string; title: string; href: string; status: Status; facts: string[]; link?: { href: string; label: string } };
  const history = $derived<Item[]>(
    [
      ...deploys.map((d) => ({
        key: d.id,
        at: d.created_at,
        title: `Deploy ${d.repo} to ${d.environment} · ${d.ref}${d.sha ? ` at ${d.sha.slice(0, 7)}` : ''}`,
        href: d.run_id ? `/activity/tasks/${encodeURIComponent(d.run_id)}` : `/chat/${encodeURIComponent(d.session_id)}`,
        status: deployStatus(d),
        facts: [`${personName(d.requested_by, names, d.requested_by_name)} asked`, d.approved_by.length || d.approved_by_names?.length ? `${approvers(d)} approved` : '', d.status === 'failed' ? '' : took(d)].filter(Boolean),
        link: d.run_url ? { href: d.run_url, label: d.status === 'failed' ? 'View logs on GitHub' : 'View on GitHub' } : undefined,
      })),
      ...denied.map((a) => {
        const p = (a.action?.payload ?? {}) as Record<string, unknown>;
        const reason = a.comment ? `: “${a.comment}”` : '';
        return {
          key: a.id,
          at: a.created_at,
          title: `Deploy ${String(p.repo ?? '')} to ${envOf(a)} · ${String(p.ref ?? '')}`,
          href: `/activity/tasks/${encodeURIComponent(a.run_id)}`,
          status: { text: 'Denied', tone: 'muted' as Tone },
          facts: [`${personName(a.approver?.requester, names, a.requested_by_name)} asked`, `${personName(a.decided_by, names, a.decided_by_name)} denied${reason}`],
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

{#snippet word(s: Status)}
  <span class="inline-flex shrink-0 items-center gap-1.5 text-small {toneClass[s.tone] || 'text-muted'}">{#if s.spin}<Spinner size={12} />{/if}{s.text}</span>
{/snippet}

{#snippet external(l: { href: string; label: string })}
  <a class="link" href={l.href} target="_blank" rel="noopener noreferrer">{l.label} ↗<span class="sr-only"> (opens in a new tab)</span></a>
{/snippet}

<Activity title="Releases" actions={setUp}>
  {#if !data}
    <Loading {failed} retry={load} />
  {:else if !data.environments.length}
    <EmptyState
      text="No deploy environments yet. Add one so Bothy can deploy with approval."
      action={session.isAdmin ? { label: 'Set up environments', href: '/settings/connections/github' } : undefined}
    />
  {:else if !history.length && !pending.length}
    <EmptyState text="No deploys yet. Ask Bothy in a chat, for example “Deploy main to staging”." />
  {:else}
    <ul>
      {#each environments as { env, last, status } (env.name)}
        <li class="border-b border-border py-2 last:border-b-0">
          <!-- Not a link to anything, so the name wraps instead of truncating; a long status moves under it. -->
          <div class="flex min-h-6 flex-wrap items-baseline justify-between gap-x-4">
            <span class="min-w-0 text-body font-medium text-text [overflow-wrap:anywhere]">{env.name} · {env.repo}</span>
            {@render word(status)}
          </div>
          {#if last}
            <p class="text-small text-muted [overflow-wrap:anywhere]">
              {version(last)} · {#if open(last)}started {relTime(last.created_at)}{:else}<time title={fullTime(last.created_at)}>{relTime(last.created_at)}</time>{/if}{#if !open(last) && (last.approved_by.length || last.approved_by_names?.length)}{` · approved by ${approvers(last)}`}{/if}{#if open(last) && last.run_url}{' · '}{@render external({ href: last.run_url, label: 'View on GitHub' })}{/if}
            </p>
          {/if}
        </li>
      {/each}
    </ul>
    <p class="mt-4 text-small text-muted">To deploy, ask Bothy in a chat, for example “Deploy {data.environments[0].repo} to {data.environments[data.environments.length - 1].name}”.</p>

    {#if history.length}
      <section class="mt-10">
        <h2 class="section-title">History</h2>
        <ul class="mt-2">
          {#each history as h (h.key)}
            <li class="flex min-h-14 flex-col justify-center border-b border-border py-2 last:border-b-0">
              <div class="flex items-baseline gap-4">
                <a href={h.href} onclick={router.link} class="-my-0.5 flex min-h-6 min-w-0 flex-1 items-center text-body font-medium text-text hover:underline pointer-coarse:-my-3 pointer-coarse:min-h-11" title={h.title}
                  ><span class="truncate">{h.title}</span></a
                >
                {@render word(h.status)}
                <time class="meta min-w-20 text-right whitespace-nowrap max-sm:hidden" title={fullTime(h.at)}>{relTime(h.at)}</time>
              </div>
              <p class="text-small text-muted [overflow-wrap:anywhere]">
                {h.facts.join(' · ')}{#if h.link}{' · '}{@render external(h.link)}{/if}<span class="sm:hidden">{' · '}<span class="whitespace-nowrap">{relTime(h.at)}</span></span>
              </p>
            </li>
          {/each}
        </ul>
      </section>
    {/if}
  {/if}
</Activity>
