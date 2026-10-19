<script lang="ts">
  import { untrack } from 'svelte';
  import { appName, busyLabel, humanize } from '../../lib/cardcopy';
  import { relTime, fullTime, serviceName } from '../../lib/format';
  import { router } from '../../lib/router.svelte';
  import { clock } from '../../lib/stores/clock.svelte';
  import { tooltip } from '../ui/tooltip';
  import Button from '../ui/Button.svelte';
  import CodeBlock from '../ui/CodeBlock.svelte';
  import Disclosure from '../ui/Disclosure.svelte';
  import DiffView from '../ui/DiffView.svelte';
  import LinkButton from '../ui/LinkButton.svelte';
  import Notice from '../ui/Notice.svelte';
  import OptionList from '../ui/OptionList.svelte';
  import Select from '../ui/Select.svelte';
  import Verbatim from '../Verbatim.svelte';
  import { allNew, decideItem, groupLines, itemQuestion } from './items';
  import type { InboxItem } from './types';

  // An inbox item that is not an action approval (§8.8), in the same card shell.
  let { item, onresolve }: { item: InboxItem; onresolve?: (choice: string, label: string) => void } = $props();

  const uid = $props.id();
  let busy = $state('');
  let failed = $state(false);
  let shown: Record<string, boolean> = $state({});
  let profile = $state('restricted');
  const profiles = [
    { value: 'restricted', title: 'Answer questions', description: "Bothy reads and answers. It can't change anything." },
    { value: 'standard', title: 'Do work, with approval', description: 'Bothy can change files, run code and send things. An approver checks each step first.' },
  ];

  const classes = [
    { value: 'read', label: 'Only reads information' },
    { value: 'local_write', label: 'Changes things inside Bothy' },
    { value: 'external_send', label: 'Sends data out' },
    { value: 'irreversible', label: "Can't be undone" },
  ];
  // The admin's choices start from what the server guessed; a refreshed item doesn't reset them.
  let effects: Record<string, string> = $state(untrack(() => (item.type === 'mcp_review' ? Object.fromEntries(item.tools.map((t) => [t.name, t.side_effect])) : {})));

  type Action = { choice: string; label: string; variant: 'primary' | 'secondary'; key?: 'allow_once' | 'deny' };
  const actions = $derived.by((): Action[] => {
    switch (item.type) {
      case 'mcp_review':
        return [
          { choice: 'approve', label: allNew(item) ? 'Approve actions' : 'Approve changes', variant: 'primary', key: 'allow_once' },
          { choice: 'turn_off', label: `Turn off ${serviceName(item.server)}`, variant: 'secondary', key: 'deny' },
        ];
      case 'skill_review':
        return [
          { choice: 'approve', label: 'Approve', variant: 'primary', key: 'allow_once' },
          { choice: 'turn_off', label: 'Turn off skill', variant: 'secondary', key: 'deny' },
        ];
      case 'unknown_sender':
        return [
          { choice: 'let_in', label: 'Let in', variant: 'primary', key: 'allow_once' },
          { choice: 'block', label: 'Block', variant: 'secondary', key: 'deny' },
        ];
      case 'group_chat':
        return [
          { choice: 'turn_on', label: 'Turn on', variant: 'primary', key: 'allow_once' },
          { choice: 'ignore', label: 'Ignore', variant: 'secondary', key: 'deny' },
        ];
    }
  });

  async function decide(a: Pick<Action, 'choice' | 'label'>) {
    if (busy) return;
    busy = a.choice;
    failed = false;
    try {
      await decideItem(item, a.choice, item.type === 'mcp_review' ? { side_effects: effects } : { tool_profile: profile });
      onresolve?.(a.choice, a.label);
    } catch {
      failed = true;
    } finally {
      busy = '';
    }
  }

  const keyHint = (a: Action) => (a.key ? `${a.label} (${a.key === 'allow_once' ? 'A' : 'D'})` : undefined);
  const changeWord: Record<string, string> = { added: 'new', removed: 'removed', changed: 'changed' };
</script>

<!-- A sentence that changed reads as prose, so the change isn't cut off by sideways scrolling. -->
{#snippet changed(key: string, diff: string, label = 'Show what changed', prose = false)}
  <p class="mt-1 text-small"><LinkButton aria-expanded={!!shown[key]} onclick={() => (shown[key] = !shown[key])}>{shown[key] ? 'Hide what changed' : label}</LinkButton></p>
  {#if shown[key]}<DiffView class="mt-2" {diff} mode={prose ? 'prose' : 'code'} />{/if}
{/snippet}

<article class="card focus-visible:outline-offset-[-1px] sm:p-5" aria-labelledby="{uid}-q" data-item={item.id} tabindex="-1">
  <h3 id="{uid}-q" class="text-heading text-text [overflow-wrap:anywhere]">{itemQuestion(item)}</h3>

  {#if item.type === 'mcp_review'}
    <ul class="mt-3">
      {#each item.tools as t (t.name)}
        <li class="border-b border-border py-3 last:border-b-0">
          <p class="text-body font-medium text-text">{t.title || humanize(t.name)}</p>
          <p class="text-small text-muted" data-copy-ignore><Verbatim text={t.description} /></p>
          <Select class="mt-2 max-w-72" label="What this does" options={classes} bind:value={effects[t.name]} />
          {#if t.diff}{@render changed(t.name, t.diff, undefined, t.prose)}{/if}
        </li>
      {/each}
    </ul>
  {:else if item.type === 'skill_review'}
    {#if item.files.length}
      <ul class="mt-3">
        {#each item.files as f (f.path)}
          <li class="border-b border-border py-3 last:border-b-0">
            <p class="text-body text-text"><span class="font-mono text-code [overflow-wrap:anywhere]" data-copy-ignore>{f.path}</span><span class="text-muted">{' · '}{changeWord[f.change] ?? 'changed'}</span></p>
            {#if f.diff}{@render changed(f.path, f.diff, f.change === 'changed' ? 'Show what changed' : 'Show the file', f.path.toLowerCase().endsWith('.md'))}{:else}<p class="mt-1 text-small text-muted">Not text, or too long to show here.</p>{/if}
          </li>
        {/each}
      </ul>
    {:else if item.noBaseline}
      <p class="mt-1 text-body text-muted">It was approved before Bothy kept a copy of approved files, so what changed can't be shown.</p>
    {/if}
    <p class="mt-1 text-small">
      <a class="link -my-0.5 inline-flex min-h-6 items-center pointer-coarse:-my-3 pointer-coarse:min-h-11" href="/knowledge/skills/{encodeURIComponent(item.skill)}" onclick={router.link}
        >{item.truncated ? "More changed than fits here. See all the skill's files" : "See the skill's files"}</a
      >
    </p>
  {:else if item.type === 'unknown_sender'}
    <p class="mt-1 text-body text-muted" data-copy-ignore>
      <Verbatim text={item.display_name} />{#if item.handle}{' ('}<Verbatim text={item.handle} />){/if}{#if item.text}{' wrote: '}“<Verbatim text={item.text.slice(0, 120)} />”{/if}
    </p>
    <Disclosure class="mt-1"><CodeBlock label="{appName(item.app)} id" code={item.external_id} /></Disclosure>
  {:else if item.type === 'group_chat'}
    {#each groupLines(item) as l (l)}<p class="mt-1 text-body text-muted" data-copy-ignore>{l}</p>{/each}
    <h4 class="mt-4 text-body font-medium text-text">What Bothy can do there</h4>
    <OptionList class="mt-1" framed={false} name="{uid}-profile" label="What Bothy can do there" options={profiles} bind:value={profile} />
    {#if item.mention_only}<p class="mt-3 text-body text-muted">Bothy answers only when mentioned.</p>{/if}
  {/if}

  {#if failed}<Notice class="mt-4" tone="danger" text="Couldn't send your decision. Try again." role="alert" />{/if}

  <div class="mt-4 flex flex-wrap gap-2 max-[30rem]:flex-col max-[30rem]:[&>.btn]:w-full">
    {#each actions as a (a.choice)}
      <Button
        variant={a.variant}
        loading={busy === a.choice}
        loadingLabel={busyLabel(a.label)}
        disabled={!!busy && busy !== a.choice}
        data-choice={a.key}
        aria-keyshortcuts={a.key ? (a.key === 'allow_once' ? 'A' : 'D') : undefined}
        onclick={() => decide(a)}
        {@attach tooltip(() => keyHint(a))}>{a.label}</Button
      >
    {/each}
  </div>
  <p class="mt-3 text-small text-muted"><time datetime={item.created_at} title={fullTime(item.created_at)}>{relTime(item.created_at, { now: clock.now })}</time></p>
</article>
