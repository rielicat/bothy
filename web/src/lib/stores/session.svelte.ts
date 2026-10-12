import { api, ApiError, onUnauthorized, setCsrf } from '../api/client';
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

class Session {
  me: Me | null = $state(null);
  setup: SetupState | null = $state(null);
  ready = $state(false);
  paused = $state(false);
  sandbox: boolean | undefined = $state();
  banners: BannerData[] = $state([]);
  dismissed: string[] = $state([]);
  today: UsageToday | null = $state(null);

  isAdmin = $derived(this.me?.user.role === 'owner' || this.me?.user.role === 'admin');
  multi = $derived(this.me?.mode === 'multi');
  principals: Principal[] = $derived((this.me?.principals ?? []).filter((p): p is Principal => !!p));
  spaces: Principal[] = $derived(this.principals.filter((p) => p.kind === 'space'));
  visibleBanners: BannerData[] = $derived(this.banners.filter((b) => !this.dismissed.includes(b.code)));

  #offs: (() => void)[] = [];

  async init() {
    onUnauthorized(() => this.expire());
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
    this.banners = me.banners ?? [];
    this.#live();
  }

  async logout() {
    try {
      await api.post('/api/auth/logout');
    } finally {
      this.#clear();
      router.navigate('/login', { replace: true });
    }
  }

  expire() {
    if (!this.me) return;
    this.#clear();
    const here = router.path + router.search;
    router.navigate(`/login?next=${encodeURIComponent(here)}`, { replace: true });
  }

  #clear() {
    this.me = null;
    setCsrf('');
    this.#offs.forEach((f) => f());
    this.#offs = [];
    events.close();
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
  }

  #live() {
    this.#offs.forEach((f) => f());
    this.#offs = [
      events.onHello((h) => {
        this.paused = h.paused;
        this.sandbox = h.sandbox;
        this.banners = h.banners ?? [];
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
      approvals.listen(),
    ];
    events.connect();
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
      this.pending = [...(p.items ?? []), ...(k.items ?? [])];
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
