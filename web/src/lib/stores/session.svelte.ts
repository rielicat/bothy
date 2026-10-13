import { api, ApiError, onCsrfRejected, onUnauthorized, setCsrf } from '../api/client';
import type {
  ApprovalData,
  ApprovalResolvedData,
  Approval,
  BannerData,
  BudgetWarningData,
  List,
  Me,
  Principal,
  SandboxStatusData,
  SetupState,
  UsageData,
  UsageToday,
} from '../api/types.gen';
import { router } from '../router.svelte';
import { events } from '../sse.svelte';

// The banner list is keyed by code in the shell.
function uniqueBanners(list: BannerData[] | null | undefined): BannerData[] {
  return [...new Map((list ?? []).filter(Boolean).map((b) => [b.code, b])).values()];
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
  dismissed: string[] = $state([]);
  today: UsageToday | null = $state(null);
  // The wizard completed setup and stays open on its last step until left.
  finishing = $state(false);

  isAdmin = $derived(this.me?.user.role === 'owner' || this.me?.user.role === 'admin');
  multi = $derived(this.me?.mode === 'multi');
  principals: Principal[] = $derived((this.me?.principals ?? []).filter((p): p is Principal => !!p));
  spaces: Principal[] = $derived(this.principals.filter((p) => p.kind === 'space'));
  visibleBanners: BannerData[] = $derived(this.banners.filter((b) => !this.dismissed.includes(b.code)));

  #offs: (() => void)[] = [];

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
    setCsrf(me.csrf);
    this.paused = me.paused;
    this.sandbox = me.sandbox_available;
    this.banners = uniqueBanners(me.banners);
    this.#live();
  }

  // Re-reads the session after the server rotated it (password change), which
  // also rotates the CSRF token that every later write must carry.
  async refresh() {
    const me = await api.get<Me>('/api/auth/me');
    this.me = me;
    setCsrf(me.csrf);
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

  setBanner(b: BannerData) {
    const rest = this.banners.filter((x) => x.code !== b.code);
    this.banners = b.clear ? rest : [...rest, b];
    if (!b.clear) this.dismissed = this.dismissed.filter((c) => c !== b.code);
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

  async load() {
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

  reset() {
    this.pending = [];
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
      events.on('approval.resolved', (e) => {
        const d = e.data as ApprovalResolvedData;
        const cur = this.pending.find((x) => x.id === d.approval_id);
        if (d.status === 'parked' && cur) this.upsert({ ...cur, status: 'parked' });
        else if (d.status !== 'pending') this.pending = this.pending.filter((x) => x.id !== d.approval_id);
      }),
    ];
    return () => offs.forEach((f) => f());
  }
}

export const approvals = new Approvals();
export const session = new Session();
