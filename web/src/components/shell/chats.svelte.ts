// The sidebar's chat list (docs/design/ui.md §4.2): stored chats grouped by
// recency, which of them Bothy is working in, and titles for chats whose
// generated title hasn't arrived yet.
import { api, qs } from '../../lib/api/client';
import type { List, MessageData, MessagePage, Run, RunStatusData, Session, SessionData } from '../../lib/api/types.gen';
import { date, isZeroTime } from '../../lib/format';
import { appendNew } from '../../lib/list';
import { events } from '../../lib/sse.svelte';
import { approvals } from '../../lib/stores/session.svelte';

/** Chats shown before "Show more", and how many more each press shows. */
export const PAGE = 30;

/** A chat's title until the generated one arrives: its first 48 characters (§4.2). */
export function firstWords(text: string, n = 48): string {
  const t = text.replace(/\s+/g, ' ').trim();
  return t.length > n ? `${t.slice(0, n).trimEnd()}…` : t;
}

export interface ChatGroup {
  key: 'today' | 'week' | 'older';
  label: string;
  items: Session[];
}

/** The viewer's chats by the day of their last message. */
export function groupChats(list: Session[], now: number, limit = Infinity): { groups: ChatGroup[]; more: boolean } {
  const day = new Date(now);
  const today = new Date(day.getFullYear(), day.getMonth(), day.getDate()).getTime();
  const week = new Date(day.getFullYear(), day.getMonth(), day.getDate() - 7).getTime();
  const groups: ChatGroup[] = [
    { key: 'today', label: 'Today', items: [] },
    { key: 'week', label: 'Previous 7 days', items: [] },
    { key: 'older', label: 'Older', items: [] },
  ];
  for (const s of list.slice(0, limit)) {
    const t = date(s.last_message_at)?.getTime() ?? 0;
    groups[t >= today ? 0 : t >= week ? 1 : 2].items.push(s);
  }
  return { groups: groups.filter((g) => g.items.length), more: list.length > limit };
}

const working = new Set(['run.queued', 'run.started']);

class Chats {
  list: Session[] = $state([]);
  loaded = $state(false);
  failed = $state(false);
  shown = $state(PAGE);
  next = 0;
  /** Chats Bothy is working in right now, by id. */
  running: Record<string, true> = $state({});
  /** The chat whose rename or delete dialog is open (one pair of dialogs serves both sidebars). */
  renaming: Session | null = $state(null);
  deleting: Session | null = $state(null);
  #words: Record<string, string> = $state({});
  #asked = new Set<string>();
  #seq = 0;

  // A chat is listed once it has a message (S14): the server may keep empty
  // ones, and an untitled chat shows only once its first words are known.
  visible: Session[] = $derived(this.list.filter((s) => !s.archived && !s.hidden && !isZeroTime(s.last_message_at) && !!this.title(s)));

  title(s: Session): string {
    return s.title.trim() || this.#words[s.id] || '';
  }

  waiting(id: string): boolean {
    return approvals.pending.some((a) => a.session_id === id);
  }

  async load(more = false) {
    const seq = ++this.#seq;
    try {
      const r = await api.get<List<Session>>(`/api/sessions${qs({ limit: 50, before: more ? this.next : undefined })}`);
      if (seq !== this.#seq) return;
      this.list = more ? appendNew(this.list, r.items, (s) => s.id) : (r.items ?? []);
      this.next = r.next ?? 0;
      this.failed = false;
      void this.#fillTitles();
    } catch {
      if (seq === this.#seq) this.failed = true;
    } finally {
      if (seq === this.#seq) this.loaded = true;
    }
  }

  async loadRunning() {
    try {
      const r = await api.get<List<Run>>('/api/runs?status=running&limit=50');
      this.running = Object.fromEntries((r.items ?? []).filter((run) => run.status === 'running' || run.status === 'queued').map((run) => [run.session_id, true]));
    } catch {
      // no spinners until the next run event
    }
  }

  showMore() {
    this.shown += PAGE;
    if (this.visible.length < this.shown && this.next) void this.load(true);
  }

  async #fillTitles() {
    const untitled = this.list.filter((s) => !s.title.trim() && !this.#asked.has(s.id) && !isZeroTime(s.last_message_at));
    await Promise.all(
      untitled.slice(0, 10).map(async (s) => {
        this.#asked.add(s.id);
        try {
          const page = await api.get<MessagePage>(`/api/sessions/${encodeURIComponent(s.id)}/messages?limit=20`);
          const first = (page.messages ?? []).find((m) => m?.role === 'user' && m.parts?.some((p) => p.text));
          const text = first?.parts.find((p) => p.text)?.text;
          if (text) this.#words[s.id] = firstWords(text);
        } catch {
          // stays hidden until a title arrives
        }
      }),
    );
  }

  rename(id: string, title: string) {
    const s = this.list.find((x) => x.id === id);
    if (s) s.title = title;
  }

  remove(id: string) {
    this.list = this.list.filter((s) => s.id !== id);
    delete this.running[id];
  }

  reset() {
    this.#seq++;
    this.list = [];
    this.loaded = false;
    this.shown = PAGE;
    this.running = {};
    this.renaming = this.deleting = null;
  }

  listen(): () => void {
    let timer: ReturnType<typeof setTimeout> | undefined;
    const soon = () => {
      clearTimeout(timer);
      timer = setTimeout(() => void this.load(), 400);
    };
    const offs = [
      events.on('session.updated', (e) => {
        const d = e.data as SessionData;
        const s = this.list.find((x) => x.id === e.session_id);
        if (s) Object.assign(s, { title: d.title, taint: d.taint, epoch: d.epoch });
        else soon();
      }),
      events.on('message.created', (e) => {
        const s = this.list.find((x) => x.id === e.session_id);
        if (!s) return soon();
        const m = (e.data as MessageData | undefined)?.message;
        const text = m?.role === 'user' ? m.parts?.find((p) => p.text)?.text : undefined;
        if (text && !s.title.trim() && !this.#words[s.id]) this.#words[s.id] = firstWords(text);
        s.last_message_at = e.at;
        this.list = [s, ...this.list.filter((x) => x !== s)];
      }),
      events.on('*', (e) => {
        if (!e.session_id) return;
        const id = e.session_id;
        const on = working.has(e.type) || (e.type === 'run.status' && (e.data as RunStatusData).status !== 'waiting_approval');
        const off = e.type === 'run.finished' || e.type === 'run.parked' || e.type === 'run.status';
        if (on) this.running[id] = true;
        else if (off) delete this.running[id];
      }),
      events.onResync(() => {
        void this.load();
        void this.loadRunning();
      }),
    ];
    return () => {
      clearTimeout(timer);
      offs.forEach((f) => f());
    };
  }
}

export const chats = new Chats();
