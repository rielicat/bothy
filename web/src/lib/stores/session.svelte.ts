import { api, ApiError, onCsrfRejected, onUnauthorized, setCsrf } from '../api/client';
import type {
  ApprovalData,
  ApprovalResolvedData,
  Approval,
  BannerData,
  BudgetWarningData,
  Inbox,
  List,
  Me,
  Principal,
  SandboxStatusData,
  SetupState,
  UsageData,
  UsageToday,
} from '../api/types.gen';
import { isZeroTime } from '../format';
import { router } from '../router.svelte';
import { events } from '../sse.svelte';

export interface PauseInfo {
  /** RFC 3339, "" when the server does not know. */
  at: string;
  /** Who paused, by name; "" when unknown. */
  by: string;
}

// The banner list is keyed by code in the shell.
// Banners whose clearing means an inbox review was decided.
const reviewBanners = new Set(['mcp_pins', 'mcp_drifted', 'skills_changed', 'unknown_sender']);

function uniqueBanners(list: BannerData[] | null | undefined): BannerData[] {
  return [...new Map((list ?? []).filter(Boolean).map((b) => [b.code, b])).values()];
}

// Banners dismissed in this tab stay dismissed until it closes (§4.9).
const DISMISSED = 'bothy.dismissed';
function readDismissed(): string[] {
  try {
    const v: unknown = JSON.parse(sessionStorage.getItem(DISMISSED) ?? '[]');
    return Array.isArray(v) ? v.filter((x): x is string => typeof x === 'string') : [];
  } catch {
    return [];
  }
}

// Approvals by id; the pending and parked lists may overlap.
function uniqueById(list: Approval[]): Approval[] {
  return [...new Map(list.filter(Boolean).map((a) => [a.id, a])).values()];
}

class Session {
  me: Me | null = $state(null);
  setup: SetupState | null = $state(null);
  ready = $state(false);
  paused = $state(false);
  sandbox: boolean | undefined = $state();
  banners: BannerData[] = $state([]);
  dismissed: string[] = $state(readDismissed());
  today: UsageToday | null = $state(null);
  // The wizard completed setup and stays open on its last step until left.
  finishing = $state(false);
  /** The session ran out (401), so sign-in says why (§13.2); cleared by the next sign-in. */
  expired = $state(false);

  isAdmin = $derived(this.me?.user.role === 'owner' || this.me?.user.role === 'admin');
  multi = $derived(this.me?.mode === 'multi');
  principals: Principal[] = $derived((this.me?.principals ?? []).filter((p): p is Principal => !!p));
  spaces: Principal[] = $derived(this.principals.filter((p) => p.kind === 'space'));
  visibleBanners: BannerData[] = $derived(this.banners.filter((b) => !this.dismissed.includes(b.code)));
  /** Who paused Bothy and when, while it is paused: the live banner's, else what sign-in said. */
  pause: PauseInfo | null = $derived.by(() => {
    if (!this.paused) return null;
    const b = this.banners.find((x) => x.code === 'paused' && !x.clear);
    const at = b?.at || this.me?.paused_at || '';
    return { at: isZeroTime(at) ? '' : at, by: b?.by_name || this.me?.paused_by_name || '' };
  });

  #offs: (() => void)[] = [];
  #held = false;

  async init() {
    onUnauthorized(() => this.expire());
    onCsrfRejected(() => this.#recheck());
    const [me, setup] = await Promise.all([
      api.get<Me>('/api/auth/me').catch((e) => (e instanceof ApiError && e.status === 401 ? null : Promise.reject(e))),
      // Non-local peers get 403 from setup routes; they can only log in.
      api.get<SetupState>('/api/setup/state').catch(() => null),
    ]).catch(() => [null, null] as const);
    this.setup = setup;
    if (me) this.login(me);
    this.ready = true;
  }

  login(me: Me) {
    this.me = me;
    this.expired = false;
    setCsrf(me.csrf);
    this.paused = me.paused;
    this.sandbox = me.sandbox_available;
    this.banners = uniqueBanners(me.banners);
    // Until a one-time password is replaced the server refuses everything else (S9).
    this.#held = !!me.user.must_change_password;
    if (!this.#held) this.#live();
  }

  // Re-reads the session after the server rotated it (password change), which
  // also rotates the CSRF token that every later write must carry.
  async refresh() {
    const me = await api.get<Me>('/api/auth/me');
    this.me = me;
    setCsrf(me.csrf);
    if (this.#held && !me.user.must_change_password) {
      this.#held = false;
      this.#live();
    }
  }

  // The cookie is shared by every tab, so another tab may have rotated the
  // session (new token) or signed someone else in. Only the first case may
  // carry on in this tab; the second leaves it signed out. Concurrent
  // refused writes share one lookup.
  #rechecking: Promise<boolean> | null = null;
  #recheck(): Promise<boolean> {
    this.#rechecking ??= (async () => {
      const was = this.me;
      if (!was) return false;
      const me = await api.get<Me>('/api/auth/me').catch(() => null);
      if (!me || this.me !== was) return false;
      if (me.user.id !== was.user.id) {
        this.expire(true);
        return false;
      }
      this.me = me;
      setCsrf(me.csrf);
      return true;
    })().finally(() => (this.#rechecking = null));
    return this.#rechecking;
  }

  async logout() {
    try {
      await api.post('/api/auth/logout');
    } finally {
      this.#clear(true);
      router.navigate('/login', { replace: true });
    }
  }

  // others: the cookie every tab shares was signed out or now belongs to
  // someone else, so this user's other tabs sign out too (and the one
  // holding the stream closes it). A plain 401 does not tell them: during
  // a password change in another tab it can be a moment's gap before the
  // new cookie, and each tab finds a dead session on its own.
  expire(others = false) {
    if (!this.me) return;
    this.expired = true;
    this.#clear(others);
    const here = router.path + router.search;
    router.navigate(`/login?next=${encodeURIComponent(here)}`, { replace: true });
  }

  #clear(others: boolean) {
    this.me = null;
    setCsrf('');
    this.#offs.forEach((f) => f());
    this.#offs = [];
    if (others) events.logout();
    else events.close();
    approvals.reset();
    this.today = null;
  }

  async refreshToday() {
    this.today = await api.get<UsageToday>('/api/usage/today').catch(() => this.today);
  }

  /** Hides a dismissible banner for the rest of this tab's life. */
  dismiss(code: string) {
    this.#saveDismissed([...this.dismissed.filter((c) => c !== code), code]);
  }

  #saveDismissed(list: string[]) {
    this.dismissed = list;
    try {
      sessionStorage.setItem(DISMISSED, JSON.stringify(list));
    } catch {
      // not kept past this page; the banner comes back on reload
    }
  }

  setBanner(b: BannerData) {
    const rest = this.banners.filter((x) => x.code !== b.code);
    this.banners = b.clear ? rest : [...rest, b];
    if (!b.clear && this.dismissed.includes(b.code)) this.#saveDismissed(this.dismissed.filter((c) => c !== b.code));
    // The kill switch announces itself only as this banner.
    if (b.code === 'paused') this.paused = !b.clear;
  }

  #live() {
    this.#offs.forEach((f) => f());
    this.#offs = [
      events.onHello((h) => {
        this.paused = h.paused;
        this.sandbox = h.sandbox;
        this.banners = uniqueBanners(h.banners);
      }),
      events.on('banner', (e) => this.setBanner(e.data as BannerData)),
      events.on('sandbox.status', (e) => (this.sandbox = (e.data as SandboxStatusData).status.available)),
      events.on('run.usage', (e) => {
        const u = e.data as UsageData;
        if (this.today) this.today = { ...this.today, spent_micros: u.day_cost_micros, limit_micros: u.day_limit_micros || this.today.limit_micros };
      }),
      events.on('budget.warning', (e) => {
        const b = e.data as BudgetWarningData;
        if (this.today && b.user_id === this.me?.user.id) this.today = { ...this.today, spent_micros: b.spent_micros, limit_micros: b.limit_micros };
      }),
      events.onResync(() => {
        void this.refreshToday();
        void approvals.load();
      }),
      // A stream the server refused (expired session) never says why; asking
      // /api/auth/me turns a 401 into a clean logout instead of endless
      // retries, and a cookie that now signs in someone else into one too.
      events.onClosed(() => {
        const was = this.me;
        void api.get<Me>('/api/auth/me').then((m) => this.me === was && m.user.id !== was?.user.id && this.expire(true), () => {});
      }),
      events.onLogout(() => this.expire()),
      approvals.listen(),
    ];
    events.connect(this.me?.user.id);
    void this.refreshToday();
    void approvals.load();
  }
}

class Approvals {
  pending: Approval[] = $state([]);
  count = $derived(this.pending.length);
  /** Inbox items the viewer can decide now (GET /api/inbox for_you), reviews and new senders included; null when the inbox can't be read. */
  forYou: number | null = $state(null);
  /** What the viewer can decide (§4.4, D40): the inbox's count, else admins anything and members what names them. */
  waiting = $derived(this.forYou ?? this.pending.filter((a) => session.isAdmin || (!a.approver?.admin_only && !!a.approver?.user_id && a.approver.user_id === session.me?.user.id)).length);
  #inbox: ReturnType<typeof setTimeout> | undefined;

  async load() {
    void this.loadInbox();
    try {
      const [p, k] = await Promise.all([
        api.get<List<Approval>>('/api/approvals?status=pending&limit=200'),
        api.get<List<Approval>>('/api/approvals?status=parked&limit=200'),
      ]);
      this.pending = uniqueById([...(p.items ?? []), ...(k.items ?? [])]);
    } catch {
      // the badge keeps its last value; the inbox shows the error
    }
  }

  async loadInbox() {
    try {
      this.forYou = ((await api.get<Inbox>('/api/inbox')).items ?? []).filter((i) => i.for_you).length;
    } catch {
      this.forYou = null;
    }
  }

  // Events come in bursts (a vote, then its resolution), so the count is asked once they settle.
  #refresh() {
    clearTimeout(this.#inbox);
    this.#inbox = setTimeout(() => void this.loadInbox(), 300);
  }

  reset() {
    this.pending = [];
    this.forYou = null;
  }

  upsert(a: Approval) {
    const open = a.status === 'pending' || a.status === 'parked';
    const rest = this.pending.filter((x) => x.id !== a.id);
    this.pending = open ? [a, ...rest] : rest;
  }

  listen(): () => void {
    const offs = [
      events.on('approval.requested', (e) => {
        const a = (e.data as ApprovalData).approval;
        if (a) this.upsert(a);
      }),
      events.on('approval.voted', (e) => {
        const a = (e.data as ApprovalData).approval;
        if (a) this.upsert(a);
      }),
      events.on('approval.resolved', (e) => {
        const d = e.data as ApprovalResolvedData;
        const cur = this.pending.find((x) => x.id === d.approval_id);
        if (d.status === 'parked' && cur) this.upsert({ ...cur, status: 'parked' });
        else if (d.status !== 'pending') this.pending = this.pending.filter((x) => x.id !== d.approval_id);
      }),
      ...(['approval.requested', 'approval.voted', 'approval.resolved', 'inbox.changed', 'mcp.pin_pending'] as const).map((t) => events.on(t, () => this.#refresh())),
      // A review decided elsewhere (Settings) only clears its banner.
      events.on('banner', (e) => {
        if (reviewBanners.has((e.data as BannerData).code)) this.#refresh();
      }),
      events.onResync(() => this.#refresh()),
    ];
    return () => offs.forEach((f) => f());
  }
}

export const approvals = new Approvals();
export const session = new Session();
