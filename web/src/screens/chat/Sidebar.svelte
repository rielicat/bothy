<script lang="ts">
  import Icon from '../../components/Icon.svelte';
  import { api, errorText, qs } from '../../lib/api/client';
  import type { List, SearchHit, Session, SessionData } from '../../lib/api/types.gen';
  import { ago, date } from '../../lib/format';
  import { originIcon } from '../../lib/labels';
  import { router } from '../../lib/router.svelte';
  import { events } from '../../lib/sse.svelte';
  import { clock } from '../../lib/stores/clock.svelte';
  import { session as auth } from '../../lib/stores/session.svelte';
  import { attempt } from '../../lib/stores/ui.svelte';

  let { activeId = '' }: { activeId?: string } = $props();

  let sessions: Session[] = $state([]);
  let next = $state(0);
  let loading = $state(true);
  let error = $state('');
  let q = $state('');
  let hits: SearchHit[] | null = $state(null);
  let searching = $state(false);

  const exposed = (s: Session) => (s.taint & 3) === 3;

  const groups = $derived.by(() => {
    const spaceNames = new Map(auth.spaces.map((p) => [p.id, p.display_name]));
    const now = new Date(clock.now);
    const startOfDay = new Date(now.getFullYear(), now.getMonth(), now.getDate()).getTime();
    const out: { title: string; items: Session[] }[] = [
      { title: 'Today', items: [] },
      { title: 'This week', items: [] },
      { title: 'Older', items: [] },
    ];
    const spaces = new Map<string, Session[]>();
    for (const s of sessions) {
      if (spaceNames.has(s.principal_id)) {
        const list = spaces.get(s.principal_id) ?? [];
        list.push(s);
        spaces.set(s.principal_id, list);
        continue;
      }
      const t = (date(s.last_message_at) ?? date(s.created_at))?.getTime() ?? 0;
      out[t >= startOfDay ? 0 : t >= startOfDay - 6 * 86400_000 ? 1 : 2].items.push(s);
    }
    const spaceGroups = [...spaces].map(([id, items]) => ({ title: spaceNames.get(id) ?? 'Space', items, space: true }));
    return [...out.filter((g) => g.items.length), ...spaceGroups];
  });

  async function load(more = false) {
    error = '';
    try {
      const r = await api.get<List<Session>>(`/api/sessions${qs({ limit: 50, before: more ? next : undefined })}`);
      sessions = more ? [...sessions, ...r.items] : r.items;
      next = r.next ?? 0;
    } catch (e) {
      error = errorText(e);
    } finally {
      loading = false;
    }
  }

  let reloadTimer: ReturnType<typeof setTimeout> | undefined;
  function reloadSoon() {
    clearTimeout(reloadTimer);
    reloadTimer = setTimeout(() => void load(), 400);
  }

  $effect(() => {
    void load();
    const offs = [
      events.on('session.updated', (e) => {
        const d = e.data as SessionData;
        const s = sessions.find((x) => x.id === e.session_id);
        if (s) Object.assign(s, { title: d.title, taint: d.taint, epoch: d.epoch });
        else reloadSoon();
      }),
      events.on('message.created', (e) => {
        const s = sessions.find((x) => x.id === e.session_id);
        if (!s) return reloadSoon();
        s.last_message_at = e.at;
        sessions = [s, ...sessions.filter((x) => x !== s)];
      }),
      events.onResync(() => void load()),
    ];
    return () => {
      clearTimeout(reloadTimer);
      offs.forEach((f) => f());
    };
  });

  let searchTimer: ReturnType<typeof setTimeout> | undefined;
  $effect(() => {
    const term = q.trim();
    clearTimeout(searchTimer);
    if (!term) {
      hits = null;
      return;
    }
    searchTimer = setTimeout(async () => {
      searching = true;
      try {
        hits = await api.get<SearchHit[]>(`/api/search${qs({ q: term, limit: 20 })}`);
      } catch (e) {
        error = errorText(e);
      } finally {
        searching = false;
      }
    }, 250);
  });

  async function newChat() {
    let s: Session | undefined;
    if (await attempt(async () => (s = await api.post<Session>('/api/sessions', {})))) {
      sessions = [s!, ...sessions];
      router.navigate(`/chat/${encodeURIComponent(s!.id)}`);
    }
  }

  // FTS5 snippet() wraps matches in [ ] (SPEC §6).
  function marks(snippet: string): { t: string; hit: boolean }[] {
    return snippet.split(/(\[[^\]]*\])/).filter(Boolean).map((t) => (t.startsWith('[') && t.endsWith(']') ? { t: t.slice(1, -1), hit: true } : { t, hit: false }));
  }
</script>

<div class="flex h-full flex-col">
  <div class="space-y-2 border-b border-line p-3">
    <button type="button" class="btn btn-primary w-full" onclick={newChat}><Icon name="plus" size={16} />New chat</button>
    <div class="relative">
      <Icon name="search" size={15} class="pointer-events-none absolute top-2.5 left-3 text-faint" />
      <input class="input pl-9" type="search" placeholder="Search all chats" aria-label="Search all chats" bind:value={q} />
    </div>
  </div>

  <div class="min-h-0 flex-1 overflow-y-auto p-2">
    {#if error}
      <p class="m-2 alert-bad" role="alert">{error}</p>
    {/if}
    {#if hits}
      <p class="px-2 py-1 text-xs text-muted" aria-live="polite">{searching ? 'Searching…' : `${hits.length} result${hits.length === 1 ? '' : 's'}`}</p>
      <ul class="space-y-0.5">
        {#each hits as h (h.session_id + ':' + h.seq)}
          <li>
            <a
              href="/chat/{encodeURIComponent(h.session_id)}#m{h.seq}"
              onclick={router.link}
              class="block rounded-lg px-3 py-2 hover:bg-sunken"
            >
              <span class="flex items-center gap-2 text-sm font-medium">
                <span class="truncate">{h.title || 'Untitled chat'}</span>
                {#if !h.trusted}<span class="chip chip-warn !py-0" title="From untrusted content">untrusted</span>{/if}
              </span>
              <span class="mt-0.5 line-clamp-2 text-xs text-muted">
                {#each marks(h.snippet) as m, i (i)}{#if m.hit}<mark class="rounded bg-warn-soft px-0.5 text-fg">{m.t}</mark>{:else}{m.t}{/if}{/each}
              </span>
            </a>
          </li>
        {/each}
      </ul>
    {:else if loading}
      <div class="space-y-2 p-2" aria-busy="true">
        {#each [1, 2, 3, 4] as i (i)}<div class="skeleton h-9"></div>{/each}
      </div>
    {:else if !sessions.length}
      <p class="px-3 py-8 text-center text-sm text-muted">No chats yet. Start one, or message Bothy from a linked chat app.</p>
    {:else}
      {#each groups as g (g.title)}
        <h2 class="mt-3 mb-1 flex items-center gap-1.5 px-3 text-xs font-medium tracking-wide text-faint uppercase first:mt-1">
          {#if 'space' in g}<Icon name="users" size={13} />{/if}{g.title}
        </h2>
        <ul class="space-y-0.5">
          {#each g.items as s (s.id)}
            <li>
              <a
                href="/chat/{encodeURIComponent(s.id)}"
                onclick={router.link}
                aria-current={s.id === activeId ? 'page' : undefined}
                class="flex items-center gap-2.5 rounded-lg px-3 py-2 text-sm hover:bg-sunken aria-[current=page]:bg-sunken aria-[current=page]:font-medium"
              >
                <Icon name={originIcon[s.origin] ?? 'chat'} size={15} class="text-faint" />
                <span class="min-w-0 flex-1 truncate">{s.title || 'New chat'}</span>
                {#if exposed(s)}<span class="size-1.5 rounded-full bg-bad" title="Exposed: private and untrusted data in context"></span>{/if}
                <span class="shrink-0 text-[11px] text-faint">{ago(s.last_message_at || s.created_at, clock.now)}</span>
              </a>
            </li>
          {/each}
        </ul>
      {/each}
      {#if next}
        <button type="button" class="btn btn-ghost btn-sm mx-auto mt-2 flex" onclick={() => load(true)}>Load older chats</button>
      {/if}
    {/if}
  </div>
</div>
