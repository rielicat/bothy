<script lang="ts">
  import Icon from '../../components/Icon.svelte';
  import LinkButton from '../../components/ui/LinkButton.svelte';
  import { api } from '../../lib/api/client';
  import { memoryUrl } from '../../lib/api/knowledge';
  import type { List, MemoryChange, MemoryData, MemoryDoc } from '../../lib/api/types.gen';
  import { events } from '../../lib/sse.svelte';
  import { session } from '../../lib/stores/session.svelte';
  import Knowledge from './Knowledge.svelte';
  import Loading from './Loading.svelte';
  import MemorySection from './MemorySection.svelte';
  import NotesHistoryDialog, { type HistoryEntry } from './NotesHistoryDialog.svelte';
  import { changeWords } from './text';

  // The documents Bothy reads at the start of every chat (§9.4, D29): what it
  // remembers for everyone, which only admins change, then the viewer's own two.
  const sections = $derived([
    {
      file: 'TEAM',
      title: 'Remembered for everyone',
      ...(session.isAdmin
        ? { note: 'Bothy uses this in every chat, for everyone, including chat apps.', empty: 'Add what Bothy should know in every chat, like your deploy day.' }
        : { note: 'Bothy uses this in every chat, for everyone. Only admins can change it.', empty: 'Nothing yet. Ask Bothy to remember something for everyone, and an admin can approve it.', readonly: true }),
      applies: "Changes apply from everyone's next message.",
    },
    { file: 'USER', title: 'About you', empty: 'Tell Bothy about your role and how you like answers.' },
    { file: 'MEMORY', title: 'Things Bothy remembers', empty: 'Bothy adds things here as you work together.' },
  ]);

  let docs = $state<Record<string, MemoryDoc> | null>(null);
  let failed = $state(false);
  let showHistory = $state(false);
  let entries = $state<HistoryEntry[] | null>(null);
  let historyFailed = $state(false);
  // "History ›" waits until there is something in it.
  let hasHistory = $state(false);

  async function load() {
    failed = false;
    try {
      const got = await Promise.all(sections.map((s) => api.get<MemoryDoc>(memoryUrl(s.file))));
      docs = Object.fromEntries(got.map((d, i) => [sections[i].file, d]));
      void Promise.all(sections.map((s) => api.get<List<MemoryChange>>(`${memoryUrl(s.file)}/history?limit=1`))).then(
        (lists) => (hasHistory ||= lists.some((l) => (l.items ?? []).length > 0)),
        () => (hasHistory = true),
      );
    } catch {
      failed = true;
    }
  }

  $effect(() => {
    void load();
    // Bothy writes memory during chats, and admins hear of each approved note for everyone;
    // a document nobody is editing follows along.
    return events.on('memory.updated', (e) => {
      const f = (e.data as MemoryData).file;
      if (docs && f in docs) void api.get<MemoryDoc>(memoryUrl(f)).then((d) => docs && (docs[f] = d), () => {});
      hasHistory = true;
    });
  });

  function who(c: MemoryChange): { who: string; chatIn?: boolean } {
    if (c.actor.startsWith('agent:')) return { who: 'Bothy', chatIn: true };
    if (c.actor === `user:${session.me?.user.id}`) return { who: 'You' };
    return { who: c.actor_name ? `${c.actor_name} (admin)` : 'An admin' };
  }

  async function openHistory() {
    showHistory = true;
    entries = null;
    historyFailed = false;
    try {
      const lists = await Promise.all(sections.map((s) => api.get<List<MemoryChange>>(`${memoryUrl(s.file)}/history?limit=25`)));
      entries = lists
        .flatMap((l, i) => (l.items ?? []).map((c) => ({ c, section: sections[i].title })))
        .sort((a, b) => b.c.at.localeCompare(a.c.at))
        .map(({ c, section }) => ({ key: `${c.file}-${c.id}`, ...who(c), what: changeWords(c.before, c.after), at: c.at, section, chat: c.chat, before: c.before, after: c.after }));
    } catch {
      historyFailed = true;
    }
  }
</script>

<Knowledge title="Memory">
  <p class="text-reading text-muted">Bothy reads this at the start of every chat. Change anything that's wrong or out of date.</p>
  {#if docs}
    {#each sections as s, i (s.file)}
      <div class={i ? 'mt-10' : 'mt-8'}>
        <MemorySection {...s} bind:doc={docs[s.file]} onsaved={() => (hasHistory = true)} />
      </div>
    {/each}
    {#if hasHistory}
      <p class="mt-10 text-body">
        <LinkButton onclick={openHistory}>History<Icon name="chevron-right" size={12} class="ml-0.5" /></LinkButton>
      </p>
    {/if}
  {:else}
    <div class="mt-8"><Loading {failed} onretry={load} /></div>
  {/if}
</Knowledge>

<NotesHistoryDialog bind:open={showHistory} {entries} failed={historyFailed} onretry={openHistory} />
