// The sidebar's chat list (docs/design/ui.md §4.2): stored chats in families,
// grouped by recency, which of them Bothy is working in, and which families
// are open.
import { api, qs } from '../../lib/api/client';
import type { List, Run, RunStatusData, Session, SessionData, SessionPage } from '../../lib/api/types.gen';
import { date, dayMonth, isZeroTime } from '../../lib/format';
import { appendNew } from '../../lib/list';
import { router } from '../../lib/router.svelte';
import { events } from '../../lib/sse.svelte';
import { approvals } from '../../lib/stores/session.svelte';

/** Rows shown before "Show more", and how many more each press shows. */
export const PAGE = 30;

/** One row of the list: a chat and the chats related to it, newest first. */
export interface ChatRow {
  /** The head's id, or "s:" and the schedule for a schedule's results. */
  key: string;
  head: Session;
  kids: Session[];
  /** The newest message in the family, which places the row. */
  at: number;
}

const time = (s: Session) => date(s.last_message_at)?.getTime() ?? 0;
const byId = (s: Session) => s.id;

/**
 * Families one level deep (§4.2): a chat hangs from its root, else from the
 * oldest chat of its line still listed, and one whose anchor holds a
 * schedule's result joins that schedule's series, headed by its newest
 * result. `parents` are anchors whose own page isn't loaded yet.
 */
export function families(list: Session[], parents: Session[] = []): ChatRow[] {
  const known = new Map([...parents, ...list].map((s) => [s.id, s]));
  const up = (s: Session) => known.get(s.root_id!) ?? known.get(s.parent_id!);
  const groups: Record<string, Session[]> = {};
  for (const s of list) {
    // Once the root is gone every chat of the line still names it, so climb
    // the parents: stopping at the first would list that one twice, nested
    // and as a head. Bounded, as the ids come from the server.
    let a = s;
    for (let i = 0, b; i++ < 50 && (b = up(a)); ) a = b;
    (groups[a.schedule_id ? `s:${a.schedule_id}` : a.id] ??= []).push(a, s);
  }
  return Object.entries(groups)
    .map(([key, g]) => {
      const all = [...new Set(g)].sort((x, y) => time(y) - time(x));
      const head = all.find((s) => s.id === key || `s:${s.schedule_id}` === key)!;
      return { key, head, kids: all.filter((s) => s !== head), at: time(all[0]) };
    })
    .sort((a, b) => b.at - a.at);
}

// What the scheduler writes after " · " in a result's title: "19 Oct", or "19 Oct, 14:05" for a later run that day.
const resultDate = /^\d{1,2} (Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec)(, \d{2}:\d{2})?$/;

/**
 * How a schedule's earlier result reads in its series: the date its title
 * ends with, in the server's words and the schedule's time zone, so it reads
 * like the head above it; a result renamed since reads as its day.
 */
export function resultLabel(s: Session, now = Date.now()): string {
  const title = s.title.trim();
  const i = title.lastIndexOf(' · ');
  const tail = i > 0 ? title.slice(i + 3) : '';
  return resultDate.test(tail) ? tail : dayMonth(s.created_at, { now });
}

export interface ChatGroup {
  key: 'today' | 'week' | 'older';
  label: string;
  items: ChatRow[];
}

/** Rows by the day of their family's last message. */
export function groupChats(rows: ChatRow[], now: number, limit = Infinity): { groups: ChatGroup[]; more: boolean } {
  const day = new Date(now);
  const today = new Date(day.getFullYear(), day.getMonth(), day.getDate()).getTime();
  const week = new Date(day.getFullYear(), day.getMonth(), day.getDate() - 7).getTime();
  const groups: ChatGroup[] = [
    { key: 'today', label: 'Today', items: [] },
    { key: 'week', label: 'Previous 7 days', items: [] },
    { key: 'older', label: 'Older', items: [] },
  ];
  for (const r of rows.slice(0, limit)) groups[r.at >= today ? 0 : r.at >= week ? 1 : 2].items.push(r);
  return { groups: groups.filter((g) => g.items.length), more: rows.length > limit };
}

const working = new Set(['run.queued', 'run.started']);
const OPEN = 'bothy.sidebar.open';

// Storage may be off (private windows, blocked site data): families then start closed.
function stored(): string[] {
  try {
    return JSON.parse(localStorage.getItem(OPEN)!) ?? [];
  } catch {
    return [];
  }
}

class Chats {
  list: Session[] = $state([]);
  /** Families' anchors that aren't on a loaded page. */
  parents: Session[] = $state([]);
  loaded = $state(false);
  failed = $state(false);
  shown = $state(PAGE);
  next = 0;
  /** Chats Bothy is working in right now, by id. */
  running: Record<string, true> = $state({});
  /** The chat whose rename or delete dialog is open (one pair of dialogs serves both sidebars and the chat menu). */
  renaming: Session | null = $state(null);
  deleting: Session | null = $state(null);
  /** Families opened or closed during this visit; the toggles' choices are also kept for the next. */
  opened: Record<string, boolean> = $state({});
  #kept = stored();
  #seq = 0;

  // A chat is listed once it has a message (S14): the server may keep empty ones, and titles the rest.
  visible: Session[] = $derived(this.list.filter((s) => !s.archived && !s.hidden && !isZeroTime(s.last_message_at)));
  rows: ChatRow[] = $derived(families(this.visible, this.parents));

  title(s: Session): string {
    return s.title.trim();
  }

  waiting(id: string): boolean {
    return approvals.pending.some((a) => a.session_id === id);
  }

  isOpen(key: string): boolean {
    return this.opened[key] ?? this.#kept.includes(key);
  }

  toggle(key: string) {
    const on = !this.isOpen(key);
    this.opened[key] = on;
    this.#kept = [...(on ? [key] : []), ...this.#kept.filter((k) => k !== key)].slice(0, 100);
    try {
      localStorage.setItem(OPEN, JSON.stringify(this.#kept));
    } catch {
      // remembered for this visit only
    }
  }

  async load(more = false) {
    const seq = ++this.#seq;
    try {
      // A page of chats can fold into a few rows, so a fresh load reads up to
      // three pages for a screenful; `more` reads the one after those loaded.
      for (let i = more ? 2 : 0; i < 3; i++) {
        const r = await api.get<SessionPage>(`/api/sessions${qs({ limit: 50, before: i ? this.next : undefined })}`);
        if (seq !== this.#seq) return;
        this.list = appendNew(i ? this.list : [], r.items, byId);
        this.parents = appendNew(i ? this.parents : [], r.parents, byId);
        this.next = r.next ?? 0;
        if (!this.next || this.rows.length >= PAGE) break;
      }
      this.failed = false;
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
    if (this.rows.length < this.shown && this.next) void this.load(true);
  }

  /** A clean chat of this one, listed under it and opened at once (§7.7). */
  async fork(id: string) {
    const n = await api.post<Session>(`/api/sessions/${id}/fork`);
    // Its notice's event may already have read the list again, with it in.
    this.list = appendNew([n], this.list, byId);
    router.navigate(`/chat/${n.id}`);
  }

  rename(id: string, title: string) {
    const s = this.list.find((x) => x.id === id);
    if (s) s.title = title;
  }

  remove(id: string) {
    this.list = this.list.filter((s) => s.id !== id);
    this.parents = this.parents.filter((s) => s.id !== id);
    delete this.running[id];
  }

  reset() {
    this.#seq++;
    this.list = [];
    this.parents = [];
    this.loaded = false;
    this.shown = PAGE;
    this.running = {};
    this.opened = {};
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
