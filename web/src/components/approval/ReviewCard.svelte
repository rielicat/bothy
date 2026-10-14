<script lang="ts">
  import { untrack } from 'svelte';
  import { api } from '../../lib/api/client';
  import type { User } from '../../lib/api/types.gen';
  import { busyLabel, humanize } from '../../lib/cardcopy';
  import { relTime, fullTime } from '../../lib/format';
  import { router } from '../../lib/router.svelte';
  import { session } from '../../lib/stores/session.svelte';
  import { clock } from '../../lib/stores/clock.svelte';
  import { tooltip } from '../ui/tooltip';
  import Button from '../ui/Button.svelte';
  import Dialog from '../ui/Dialog.svelte';
  import DiffView from '../ui/DiffView.svelte';
  import LinkButton from '../ui/LinkButton.svelte';
  import Notice from '../ui/Notice.svelte';
  import Select from '../ui/Select.svelte';
  import Verbatim from '../Verbatim.svelte';
  import { decideItem, itemQuestion } from './items';
  import type { InboxItem } from './types';

  // An inbox item that is not an action approval (§8.8), in the same card shell.
  let { item, onresolve }: { item: InboxItem; onresolve?: (choice: string, label: string) => void } = $props();

  const uid = $props.id();
  let busy = $state('');
  let failed = $state(false);
  let shown: Record<string, boolean> = $state({});
  let linking = $state(false);
  let person = $state('');
  let users: User[] = $state([]);

  const classes = [
    { value: 'read', label: 'Only reads information' },
    { value: 'local_write', label: 'Changes things inside Bothy' },
    { value: 'external_send', label: 'Sends data out' },
    { value: 'irreversible', label: "Can't be undone" },
  ];
  // The admin's choices start from what the server guessed; a refreshed item doesn't reset them.
  let effects: Record<string, string> = $state(untrack(() => (item.type === 'mcp_review' ? Object.fromEntries(item.tools.map((t) => [t.name, t.side_effect])) : {})));

  type Action = { choice: string; label: string; variant: 'primary' | 'secondary'; key?: 'allow_once' | 'deny'; open?: () => void };
  const actions = $derived.by((): Action[] => {
    switch (item.type) {
      case 'mcp_review':
        return [
          { choice: 'approve', label: 'Approve tools', variant: 'primary', key: 'allow_once' },
          { choice: 'turn_off', label: `Turn off ${humanize(item.server)}`, variant: 'secondary', key: 'deny' },
        ];
      case 'skill_review':
        return [
          { choice: 'approve', label: 'Approve', variant: 'primary', key: 'allow_once' },
          { choice: 'turn_off', label: 'Turn off skill', variant: 'secondary', key: 'deny' },
        ];
      case 'unknown_sender':
        return [
          { choice: 'guest', label: 'Let them chat as a guest', variant: 'primary', key: 'allow_once' },
          { choice: 'link', label: 'Link to a person…', variant: 'secondary', open: openLink },
          { choice: 'block', label: 'Block', variant: 'secondary' },
        ];
      case 'group_chat':
        return [
          { choice: 'turn_on', label: 'Turn on', variant: 'primary', key: 'allow_once' },
          { choice: 'ignore', label: 'Ignore', variant: 'secondary', key: 'deny' },
        ];
    }
  });

  async function decide(a: Pick<Action, 'choice' | 'label'>, extra: { user_id?: string } = {}) {
    if (busy) return;
    busy = a.choice;
    failed = false;
    try {
      await decideItem(item, a.choice, { ...(item.type === 'mcp_review' ? { side_effects: effects } : {}), ...extra });
      linking = false;
      onresolve?.(a.choice, a.label);
    } catch {
      failed = true;
    } finally {
      busy = '';
    }
  }

  function openLink() {
    linking = true;
    if (!users.length && session.isAdmin) void api.get<User[]>('/api/users').then((u) => (users = u), () => {});
  }

  const keyHint = (a: Action) => (a.key ? `${a.label} (${a.key === 'allow_once' ? 'A' : 'D'})` : undefined);
</script>

{#snippet changed(key: string, diff: string, label = 'Show what changed')}
  <p class="mt-1 text-small"><LinkButton aria-expanded={!!shown[key]} onclick={() => (shown[key] = !shown[key])}>{shown[key] ? 'Hide what changed' : label}</LinkButton></p>
  {#if shown[key]}<DiffView class="mt-2" {diff} />{/if}
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
          {#if t.diff}{@render changed(t.name, t.diff)}{/if}
        </li>
      {/each}
    </ul>
  {:else if item.type === 'skill_review'}
    <!-- The server sends the skill's metadata only; its files are on the skill's page. -->
    <p class="mt-1 text-small"><a class="link" href="/knowledge/skills/{encodeURIComponent(item.skill)}" onclick={router.link}>See the skill's files</a></p>
  {:else if item.type === 'unknown_sender'}
    <p class="mt-1 text-body text-muted" data-copy-ignore>
      <Verbatim text={item.display_name} /> (<Verbatim text={item.handle} />){#if item.text}{' wrote: '}“<Verbatim text={item.text.slice(0, 120)} />”{/if}
    </p>
  {:else if item.type === 'group_chat'}
    <p class="mt-1 text-body text-muted">{item.added_by ? `Added by ${item.added_by}. ` : ''}Bothy will answer only when mentioned.</p>
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
        onclick={() => (a.open ? a.open() : decide(a))}
        {@attach tooltip(() => keyHint(a))}>{a.label}</Button
      >
    {/each}
  </div>
  <p class="mt-3 text-small text-muted"><time datetime={item.created_at} title={fullTime(item.created_at)}>{relTime(item.created_at, { now: clock.now })}</time></p>
</article>

{#if item.type === 'unknown_sender'}
  <Dialog bind:open={linking} title="Link to a person" busy={busy === 'link'}>
    <Select label="Person" options={[{ value: '', label: 'Choose a person' }, ...users.map((u) => ({ value: u.id, label: u.display_name || u.username }))]} bind:value={person} />
    <p class="mt-2 text-small text-muted">Their messages will count as that person's, with the same permissions.</p>
    {#snippet footer({ close })}
      <Button data-cancel disabled={busy === 'link'} onclick={close}>Cancel</Button>
      <Button variant="primary" reason={person ? undefined : 'Choose a person first.'} loading={busy === 'link'} loadingLabel="Linking…" onclick={() => decide({ choice: 'link', label: 'Linked' }, { user_id: person })}>Link</Button>
    {/snippet}
  </Dialog>
{/if}
