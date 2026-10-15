<script lang="ts">
  import Button from '../../components/ui/Button.svelte';
  import CodeBlock from '../../components/ui/CodeBlock.svelte';
  import EmptyState from '../../components/ui/EmptyState.svelte';
  import LinkButton from '../../components/ui/LinkButton.svelte';
  import Spinner from '../../components/ui/Spinner.svelte';
  import {
    failReason,
    githubState,
    knowledge,
    progressPct,
    projectHref,
    queuedRepo,
    readRepos,
    repoState,
    type KnowledgeRepo,
  } from '../../lib/api/knowledge';
  import { fullTime, relTime } from '../../lib/format';
  import { router } from '../../lib/router.svelte';
  import { events } from '../../lib/sse.svelte';
  import { session } from '../../lib/stores/session.svelte';
  import Knowledge from './Knowledge.svelte';
  import Loading from './Loading.svelte';
  import { inSentence } from './text';

  const GITHUB = '/settings/connections/github';

  let rows = $state<KnowledgeRepo[] | null>(null);
  let connected = $state<boolean | null>(null);
  let failed = $state(false);
  let details = $state<Record<string, boolean>>({});
  let rowError = $state<Record<string, string>>({});

  async function load() {
    failed = false;
    try {
      // A server without the knowledge module shows how to connect GitHub, not an error.
      const [repos, gh] = await Promise.all([readRepos(), githubState(session.isAdmin)]);
      const known = new Set(repos.map((r) => r.repo.toLowerCase()));
      rows = [...repos, ...gh.allowed.filter((n) => !known.has(n)).map(queuedRepo)];
      connected = gh.connected;
    } catch {
      failed = true;
    }
  }

  $effect(() => {
    void load();
    return events.on('knowledge.updated', (e) => {
      const d = e.data as { repo: KnowledgeRepo; deleted?: boolean };
      if (!rows || !d?.repo) return;
      const i = rows.findIndex((r) => r.repo.toLowerCase() === d.repo.repo.toLowerCase());
      if (d.deleted) rows = rows.filter((_, j) => j !== i);
      else rows = i < 0 ? [...rows, d.repo] : rows.map((r, j) => (j === i ? d.repo : r));
    });
  });

  async function readAgain(r: KnowledgeRepo) {
    rowError = { ...rowError, [r.repo]: '' };
    try {
      const next = await knowledge.reindex(r.id);
      rows = (rows ?? []).map((x) => (x.repo === r.repo ? next : x));
    } catch {
      rowError = { ...rowError, [r.repo]: "Couldn't start reading. Try again." };
    }
  }

  // A connected repository this person's Bothy hasn't read: indexes are per
  // person, so nothing reads it until they ask (or a chat checks it out).
  let requested = $state<Record<string, boolean>>({});
  const unread = (r: KnowledgeRepo) => r.id === 0 && !requested[r.repo];

  async function readNow(r: KnowledgeRepo) {
    rowError = { ...rowError, [r.repo]: '' };
    requested = { ...requested, [r.repo]: true };
    try {
      await knowledge.readConnected([r.repo]);
    } catch {
      requested = { ...requested, [r.repo]: false };
      rowError = { ...rowError, [r.repo]: "Couldn't start reading. Try again." };
    }
  }

  function line2(r: KnowledgeRepo): string {
    if (unread(r)) return "Bothy hasn't read this project for you yet.";
    const summary = r.summary;
    if (repoState(r) === 'failed') return failReason(r);
    if (r.notes.status !== 'ready') return summary || 'Bothy is writing notes for this project.';
    const updated = `Notes updated ${inSentence(relTime(r.notes.updated_at))}.`;
    return summary ? `${summary} ${updated}` : updated;
  }
</script>

{#snippet actions()}
  <!-- An empty list offers "Choose repositories" itself; a second button to the same place would only repeat it. -->
  {#if session.isAdmin && connected && rows?.length}<Button href="{GITHUB}#repositories">Add repositories</Button>{/if}
{/snippet}

<Knowledge {actions}>
  {#if !rows}
    <Loading {failed} onretry={load} />
  {:else if !rows.length}
    {#if !session.isAdmin}
      <EmptyState text="Ask an admin to connect GitHub so Bothy can read your code." />
    {:else if connected}
      <EmptyState text="Choose which repositories Bothy can read." action={{ label: 'Choose repositories', href: `${GITHUB}#repositories` }} />
    {:else}
      <EmptyState
        text="Connect GitHub to give Bothy your code."
        more="It reads your repositories, keeps notes on each project and can answer questions about them."
        action={{ label: 'Connect GitHub', href: GITHUB }}
      />
    {/if}
  {:else}
    <ul aria-label="Projects">
      {#each rows as r (r.repo)}
        {@const st = repoState(r)}
        {@const pct = progressPct(r)}
        <li class="border-b border-border last:border-b-0">
          <!-- The whole row opens the project; the link stretches over it and the actions sit above it. -->
          <div
            class="relative -mx-2 grid min-h-14 grid-cols-[minmax(0,1fr)_auto] items-baseline gap-x-4 rounded-md px-2 py-2 hover:bg-subtle has-[a:focus-visible]:outline-2 has-[a:focus-visible]:-outline-offset-2 has-[a:focus-visible]:outline-focus pointer-coarse:py-3"
          >
            <!-- The negative margins grow the link's own box to a full target without moving the lines. -->
            <a
              href={projectHref(r.repo)}
              title={r.repo}
              class="-my-0.5 block truncate py-0.5 text-body font-medium text-text after:absolute after:inset-0 after:rounded-md focus-visible:outline-none pointer-coarse:-my-3 pointer-coarse:py-3"
              onclick={router.link}>{r.repo}</a
            >
            <span class="flex items-center gap-1.5 text-small {st === 'failed' ? 'text-danger-text' : 'text-muted'} tabular-nums">
              {#if unread(r)}Not read yet{:else if st === 'ready'}Up to date{:else if st === 'stale'}Out of date{:else if st === 'failed'}Couldn't read{:else}<Spinner size={12} />Reading…{#if pct !== null}{' '}{pct}%{/if}{/if}
            </span>
            <div class="col-span-2 flex items-baseline gap-4">
              <p class="min-w-0 flex-1 text-small text-muted {st === 'failed' ? '' : 'truncate'}" title={r.notes.status === 'ready' ? fullTime(r.notes.updated_at) : undefined}>
                {#if rowError[r.repo]}<span class="text-danger-text">{rowError[r.repo]}</span>{:else}{line2(r)}{/if}
              </p>
              {#if unread(r) || (st === 'stale' && session.isAdmin) || (st === 'failed' && r.error)}
                <span class="relative z-10 shrink-0 text-small">
                  {#if unread(r)}
                    <LinkButton onclick={() => readNow(r)}>Read now</LinkButton>
                  {:else if st === 'stale'}
                    <LinkButton onclick={() => readAgain(r)}>Read again</LinkButton>
                  {:else}
                    <button
                      type="button"
                      class="-my-0.5 inline-flex min-h-6 cursor-pointer items-center text-muted hover:text-text pointer-coarse:-my-3 pointer-coarse:min-h-11 pointer-coarse:min-w-11 pointer-coarse:justify-center"
                      aria-expanded={!!details[r.repo]}
                      aria-controls="why-{r.id}"
                      onclick={() => (details = { ...details, [r.repo]: !details[r.repo] })}>Details</button
                    >
                  {/if}
                </span>
              {/if}
            </div>
          </div>
          {#if st === 'failed' && details[r.repo]}
            <div id="why-{r.id}" class="pb-3" data-details>
              <CodeBlock code={r.error ?? ''} wrap />
              {#if session.isAdmin}<LinkButton class="mt-2 text-small" onclick={() => readAgain(r)}>Read again</LinkButton>{/if}
            </div>
          {/if}
        </li>
      {/each}
    </ul>
  {/if}
</Knowledge>
