<script lang="ts">
  import { tick, untrack } from 'svelte';
  import Icon from '../../../components/Icon.svelte';
  import { Button, CodeBlock, Disclosure, EmptyState, Page, toast } from '../../../components/ui';
  import { api } from '../../../lib/api/client';
  import type { Grant, List, Schedule, Session } from '../../../lib/api/types.gen';
  import { fullTime, relTime } from '../../../lib/format';
  import { session } from '../../../lib/stores/session.svelte';
  import { describeGrant, grantMeta } from './grantText';
  import { capital, relLower, securityBack } from './page.svelte';
  import PageState from './PageState.svelte';

  let grants = $state<Grant[] | null>(null);
  let failed = $state(false);
  let titles = $state<Record<string, string>>({});
  let removing = $state('');
  let error = $state<{ id: string; text: string } | null>(null);
  let list: HTMLElement | undefined = $state();

  async function load() {
    failed = false;
    try {
      const g = (await api.get<Grant[]>('/api/grants')) ?? [];
      grants = [...g].sort((a, b) => b.created_at.localeCompare(a.created_at));
      void names(grants);
    } catch {
      failed = true;
    }
  }

  // Chat and schedule names for grants limited to one of them; ids never show.
  async function names(g: Grant[]) {
    const out: Record<string, string> = {};
    if (g.some((x) => x.scope === 'session')) {
      const s = await api.get<List<Session>>('/api/sessions?limit=200').catch(() => null);
      for (const x of s?.items ?? []) if (x.title) out[x.id] = x.title;
    }
    if (g.some((x) => x.scope === 'schedule')) {
      const s = await api.get<Schedule[]>('/api/schedules').catch(() => null);
      for (const x of s ?? []) out[x.id] = x.name;
    }
    titles = out;
  }

  $effect(() => {
    if (session.isAdmin) untrack(load);
  });

  async function remove(g: Grant, i: number) {
    removing = g.id;
    error = null;
    try {
      await api.del(`/api/grants/${encodeURIComponent(g.id)}`);
      grants = (grants ?? []).filter((x) => x.id !== g.id);
      toast('Permission removed.');
      // Focus stays in the list: the next row's Remove, else the previous one, else the page title.
      await tick();
      const buttons = list?.querySelectorAll<HTMLElement>('[data-remove]') ?? [];
      (buttons[Math.min(i, buttons.length - 1)] ?? document.querySelector<HTMLElement>('h1'))?.focus();
    } catch {
      error = { id: g.id, text: "Couldn't remove this permission. Try again." };
    } finally {
      removing = '';
    }
  }
</script>

<Page title="Allowed without asking" back={securityBack}>
  {#if !session.isAdmin}
    <PageState kind="forbidden" />
  {:else if failed}
    <PageState kind="failed" onretry={load} />
  {:else if grants === null}
    <PageState kind="loading" />
  {:else if !grants.length}
    <EmptyState text="Nothing is allowed without asking yet." more="When someone always allows an action on a request, it shows up here." />
  {:else}
    <div bind:this={list}>
      {#each grants as g, i (g.id)}
        {@const d = describeGrant(g)}
        {@const meta = grantMeta(g, { principals: session.principals, titles, when: '' })}
        <div class="border-b border-border py-2 last:border-b-0">
          <div class="flex min-h-10 items-center gap-3 pointer-coarse:min-h-12">
            <div class="min-w-0 flex-1">
              <p class="text-body text-text [overflow-wrap:anywhere]">
                {d.what}{#if d.exact}{' '}<span class="font-mono text-code" data-copy-ignore>{d.exact}</span>{/if}
              </p>
              <!-- The non-breaking space keeps each "·" with the piece before it, so a wrapped line never starts with one. -->
              <p class="text-small text-muted">
                {#each meta as m, i (m)}{i === 0 ? capital(m) : m}{'\u00a0· '}{/each}<time class="whitespace-nowrap" datetime={g.created_at} title={fullTime(g.created_at)}
                  >{meta.length ? relLower(g.created_at) : relTime(g.created_at)}</time
                >
              </p>
            </div>
            <Button variant="ghost" size="sm" data-remove loading={removing === g.id} loadingLabel="Removing…" onclick={() => remove(g, i)}>Remove</Button>
          </div>
          {#if d.raw}<Disclosure><CodeBlock code={d.raw} wrap /></Disclosure>{/if}
          {#if error?.id === g.id}<p class="field-error" role="alert"><Icon name="alert" class="mt-px" />{error.text}</p>{/if}
        </div>
      {/each}
    </div>
  {/if}
</Page>
