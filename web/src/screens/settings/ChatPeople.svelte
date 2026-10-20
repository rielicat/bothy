<script lang="ts">
  import { untrack } from 'svelte';
  import { Avatar, EmptyState, IconButton, Menu, Notice, Select, SettingsRow, SettingsSection, StatusLine, Switch, TextField, toast, type MenuItem } from '../../components/ui';
  import { api } from '../../lib/api/client';
  import type { ChannelStatus, Teammate } from '../../lib/api/types.gen';
  import { joinNames } from '../../lib/cardcopy';
  import { money, relTime } from '../../lib/format';
  import { events } from '../../lib/sse.svelte';
  import { session } from '../../lib/stores/session.svelte';
  import ConfirmDialog from './ConfirmDialog.svelte';
  import { personalLimit, Saver, settingsConfig } from './store.svelte';

  // Settings → People → In chat apps (docs/design/ui.md §11.5): people Bothy
  // answers in Slack, Discord and Telegram, who can approve, and blocking.
  let {
    onlimit,
    count = $bindable(null),
  }: {
    onlimit: (key: string, name: string) => void;
    /** How many people are listed, once the list loaded (0 when it didn't). */
    count?: number | null;
  } = $props();

  const apps: Record<string, string> = { slack: 'Slack', telegram: 'Telegram', discord: 'Discord' };
  let people = $state<Teammate[]>([]);
  // null: Bothy couldn't say which chat apps are connected.
  let channels = $state<ChannelStatus[] | null>(null);
  let phase = $state<'loading' | 'ready' | 'failed'>('loading');
  let rowError = $state('');
  let q = $state('');
  let narrow = $state(false);

  // A reload on inbox.changed keeps the rows where they are, so a row the
  // pointer is on doesn't move away; only a first load or Try again sorts.
  async function load(keepOrder = false) {
    try {
      const [next, chans] = await Promise.all([api.get<Teammate[]>('/api/teammates'), api.get<ChannelStatus[]>('/api/channels').catch(() => null)]);
      if (keepOrder) {
        const at = new Map(people.map((t, i) => [key(t), i]));
        next.sort((a, b) => (at.get(key(a)) ?? at.size) - (at.get(key(b)) ?? at.size));
      }
      [people, channels] = [next, chans];
      phase = 'ready';
      count = next.length;
    } catch {
      if (!keepOrder || phase !== 'ready') {
        phase = 'failed';
        count ??= 0;
      }
    }
  }

  $effect(() => {
    untrack(() => load());
    return events.on('inbox.changed', () => void load(true));
  });

  // Below 640 px the switch gives way to a menu item.
  $effect(() => {
    const mq = window.matchMedia?.('(max-width: 639px)');
    if (!mq) return;
    const sync = () => (narrow = mq.matches);
    sync();
    mq.addEventListener('change', sync);
    return () => mq.removeEventListener('change', sync);
  });

  // The 0013 upgrade's reminder, until an admin changes someone here or a week passes (§4.9: not a banner).
  const teamAccess = $derived(session.isAdmin && session.banners.some((b) => b.code === 'team_access'));
  const key = (t: Teammate) => `${t.channel}:${t.external_id}`;
  const uid = $props.id();
  const app = (t: Teammate) => apps[t.channel] ?? t.channel;
  const daily = $derived(settingsConfig.value<number>('budgets.daily_usd', 0));
  const memberDefault = $derived(settingsConfig.value<number>('budgets.member_daily_usd', 0));
  const dollars = (v: number) => money(Math.round(v * 1e6));
  const limitOf = (t: Teammate) => personalLimit(key(t)) ?? memberDefault;
  const nobodyApproves = $derived(people.length > 0 && !people.some((t) => t.status === 'teammate' && t.can_approve));
  const shown = $derived.by(() => {
    const s = q.trim().toLowerCase();
    return s ? people.filter((t) => t.name.toLowerCase().includes(s) || (t.handle ?? '').toLowerCase().includes(s)) : people;
  });

  // The empty list says where to go next: connect an app, or message Bothy
  // there (Telegram, and Discord while Bothy can't check its members, also
  // need an admin to let people in).
  const connected = $derived(channels && Object.keys(apps).filter((n) => channels!.some((c) => c.name === n && c.enabled)));
  const noApps = $derived(!!connected && !connected.length);
  const orNames = (names: string[]) => (names.length > 1 ? `${names.slice(0, -1).join(', ')} or ${names[names.length - 1]}` : names[0]);
  const membersChecked = (n: string) => n === 'slack' || (n === 'discord' && channels?.find((c) => c.name === n)?.member_check === 'ok');
  const letIn = $derived((connected ?? []).filter((n) => !membersChecked(n)).map((n) => apps[n]));
  const emptyMore = $derived(
    `Message Bothy in ${connected?.length ? orNames(connected.map((n) => apps[n])) : 'a chat app'} to appear here.${letIn.length ? ` On ${joinNames(letIn)}, let yourself in from Approvals first.` : ''}`,
  );

  function leftText(t: Teammate): string {
    if (t.channel === 'discord') return 'No longer in the server';
    const team = channels?.find((c) => c.name === t.channel)?.team;
    return team ? `No longer in ${team}` : 'No longer in the workspace';
  }

  function meta(t: Teammate): string {
    // Two people of one app with the same name and no username are told apart by their account id there.
    const twin = !t.handle && people.some((o) => o !== t && o.channel === t.channel && o.name === t.name) ? `ID ${t.external_id}` : '';
    if (t.status === 'blocked') return [app(t), twin, 'Blocked'].filter(Boolean).join(' · ');
    const handle = t.handle && t.name !== `@${t.handle}` ? `@${t.handle}` : '';
    // On phones the switch is in the menu, so the row says who can approve.
    return [handle, app(t), twin, t.left ? leftText(t) : '', narrow && t.can_approve ? 'Can approve' : ''].filter(Boolean).join(' · ');
  }

  function active(t: Teammate): string {
    const limit = limitOf(t);
    const spent = money(t.spent_today_micros);
    const seen = relTime(t.last_seen_at).replace(/^(Yesterday|Tomorrow)/, (w) => w.toLowerCase());
    return `Active ${seen} · ${limit > 0 ? `${spent} of ${dollars(limit)}` : spent} today`;
  }

  const path = (t: Teammate) => `/api/identities/${encodeURIComponent(t.channel)}/${encodeURIComponent(t.external_id)}`;

  async function patch(t: Teammate, body: Record<string, unknown>, done: string): Promise<boolean> {
    rowError = '';
    try {
      Object.assign(t, await api.patch<Teammate>(path(t), body));
      toast(done);
      return true;
    } catch {
      rowError = key(t);
      return false;
    }
  }

  async function setApprove(t: Teammate, v: boolean) {
    t.can_approve = v;
    if (!(await patch(t, { can_approve: v }, 'Saved.'))) t.can_approve = !v;
  }

  function items(t: Teammate): MenuItem[] {
    const forget = { label: 'Delete their data…', danger: true, onselect: () => (deleting = t) };
    if (t.status === 'blocked') return [{ label: 'Unblock', onselect: () => patch(t, { status: 'teammate' }, 'Unblocked.') }, forget];
    return [
      ...(narrow
        ? [{ label: 'Can approve', checked: t.can_approve, toggle: true, disabled: t.left, reason: t.left ? leftText(t) : undefined, onselect: () => setApprove(t, !t.can_approve) }]
        : []),
      { label: 'Daily limit…', onselect: () => onlimit(key(t), t.name) },
      { label: 'Block…', danger: true, onselect: () => (blocking = t) },
      ...(t.left ? [forget] : []),
    ];
  }

  let blocking = $state<Teammate | null>(null);
  let deleting = $state<Teammate | null>(null);
  let busy = $state(false);
  let dialogError = $state('');

  async function block() {
    const t = blocking;
    if (!t) return;
    busy = true;
    dialogError = '';
    try {
      // Updated in place: a reload would move the row to the blocked ones under the pointer.
      Object.assign(t, await api.patch<Teammate>(path(t), { status: 'blocked' }));
      blocking = null;
      toast('Blocked.');
    } catch {
      dialogError = "Couldn't save. Try again.";
    } finally {
      busy = false;
    }
  }

  async function forget() {
    const t = deleting;
    if (!t) return;
    busy = true;
    dialogError = '';
    try {
      await api.del(`/api/teammates/${encodeURIComponent(t.channel)}/${encodeURIComponent(t.external_id)}`);
      deleting = null;
      toast('Deleted.');
      void load(true);
    } catch {
      dialogError = "Bothy couldn't delete their data. Try again.";
    } finally {
      busy = false;
    }
  }

  const share = new Saver();
  const shareValue = $derived(String(settingsConfig.value<number>('budgets.chat_share_pct', 60)));
  const shareOptions = $derived.by(() => {
    const opts = [
      { value: '40', label: '40%' },
      { value: '60', label: '60% (recommended)' },
      { value: '80', label: '80%' },
      { value: '100', label: 'All of it' },
    ];
    return opts.some((o) => o.value === shareValue) ? opts : [...opts, { value: shareValue, label: `${shareValue}%` }];
  });
</script>

<SettingsSection title="In chat apps" bare>
  {#if teamAccess}<Notice class="mb-3" text="People in chat apps no longer link to Bothy accounts. Check who can use Bothy and who can approve below." />{/if}
  <!-- Without a chat app there's nobody to approve and nothing to share, so only the way to connect one shows. -->
  {#if !noApps}
    <p class="text-small text-muted">People who can approve get approval requests in their direct messages. Changes to Bothy's settings still need an admin here.</p>
  {/if}
  {#if phase === 'failed'}
    <StatusLine class="mt-3" tone="danger" text="This list didn't load. Check your connection and try again." action={{ label: 'Try again', onclick: () => load() }} />
  {:else if phase === 'ready'}
    {#if daily > 0 && !noApps}
      <div class="list-card mt-3">
        <SettingsRow label="Share of the daily limit for chat apps" help="Leaves the rest of Bothy's {dollars(daily)} daily limit for the web." status={share.status} error={share.error}>
          {#snippet children(c)}
            <Select
              id={c.id}
              aria-describedby={c.describedBy}
              value={shareValue}
              options={shareOptions}
              onchange={(e) => {
                const v = Number(e.currentTarget.value);
                void share.run(() => settingsConfig.save({ 'budgets.chat_share_pct': v }));
              }}
            />
          {/snippet}
        </SettingsRow>
      </div>
    {/if}
    {#if !people.length}
      {#if noApps}
        <EmptyState class="mt-3" text="No chat app is connected yet." action={{ label: 'Connect a chat app', href: '/settings/connections', variant: 'secondary' }} />
      {:else}
        <EmptyState
          class="mt-3"
          text="Nobody uses Bothy in a chat app yet."
          more={emptyMore}
          action={letIn.length ? { label: 'Open Approvals', href: '/approvals', variant: 'secondary' } : undefined}
        />
      {/if}
    {:else}
      {#if nobodyApproves}<p class="mt-3 text-body text-text">Nobody can approve in chat apps yet, so requests from chat apps wait for an admin in Approvals.</p>{/if}
      {#if people.length > 20}<TextField class="mt-3" label="Find someone" type="search" bind:value={q} />{/if}
      <ul class="list-card mt-4">
        {#each shown as t (key(t))}
          <li class="border-b border-border last:border-b-0">
            <div class="flex min-h-14 items-center gap-3 py-2">
              <Avatar name={t.name} size={32} />
              <div class="min-w-0 flex-1">
                <p class="truncate text-body font-medium text-text" title={t.name} data-copy-ignore>{t.name}</p>
                <p id="{uid}-meta-{key(t)}" class="truncate text-small text-muted" data-copy-ignore>{meta(t)}</p>
                {#if t.status !== 'blocked'}<p class="text-small text-muted tabular-nums">{active(t)}</p>{/if}
              </div>
              {#if t.status !== 'blocked' && !narrow}
                <Switch
                  class="max-w-80 flex-wrap justify-end gap-y-0.5"
                  label="Can approve"
                  aria-label="Can approve: {t.name}"
                  checked={t.can_approve}
                  disabled={t.left}
                  aria-describedby={t.left ? `${uid}-meta-${key(t)}` : undefined}
                  onchange={(v) => setApprove(t, v)}
                />
              {/if}
              <div class="flex size-8 shrink-0 items-center justify-center pointer-coarse:size-11">
                <Menu items={items(t)} placement="bottom-end">
                  {#snippet trigger(props)}<IconButton icon="more" label="Options for {t.name}" size="sm" variant="quiet" {...props} />{/snippet}
                </Menu>
              </div>
            </div>
            {#if rowError === key(t)}<StatusLine class="pb-2" tone="danger" text="Couldn't save. Try again." alert />{/if}
          </li>
        {:else}
          <li class="py-3 text-body text-muted">No one matches “{q.trim()}”.</li>
        {/each}
      </ul>
    {/if}
  {/if}
</SettingsSection>

<ConfirmDialog
  open={!!blocking}
  title="Block {blocking?.name}?"
  confirm="Block"
  busyLabel="Blocking…"
  {busy}
  error={dialogError}
  onconfirm={block}
  onclose={() => {
    blocking = null;
    dialogError = '';
  }}>Bothy stops answering them in {blocking ? app(blocking) : ''} and stops anything it's doing for them. Their chats are kept.</ConfirmDialog
>

<ConfirmDialog
  open={!!deleting}
  title="Delete {deleting?.name}'s data?"
  confirm="Delete"
  busyLabel="Deleting…"
  {busy}
  error={dialogError}
  onconfirm={forget}
  onclose={() => {
    deleting = null;
    dialogError = '';
  }}>This deletes their chats, files, notes and schedules in Bothy. It can't be undone.{deleting?.status === 'blocked' ? ' They stay blocked.' : ''}</ConfirmDialog
>
