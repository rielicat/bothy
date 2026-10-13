<script lang="ts">
  import ChannelSetup from '../../components/ChannelSetup.svelte';
  import ConfigField from '../../components/ConfigField.svelte';
  import Icon from '../../components/Icon.svelte';
  import Pairing from '../../components/Pairing.svelte';
  import Section from '../../components/Section.svelte';
  import { api } from '../../lib/api/client';
  import type { ChannelIdentity, ChannelStatus, ChannelStatusData, ChannelTestResp, IdentityStatus, Space, User } from '../../lib/api/types.gen';
  import { config } from '../../lib/config.svelte';
  import { ago } from '../../lib/format';
  import { channelName, originIcon } from '../../lib/labels';
  import { events } from '../../lib/sse.svelte';
  import { session } from '../../lib/stores/session.svelte';
  import { attempt, toasts } from '../../lib/stores/ui.svelte';

  let channels = $state<ChannelStatus[]>([]);
  let spaces = $state<Space[]>([]);
  let identities = $state<ChannelIdentity[]>([]);
  let users = $state<User[]>([]);
  let open = $state('');
  let linkTo = $state<Record<string, string>>({});

  const names = ['telegram', 'discord', 'slack'] as const;
  const linked = $derived(identities.filter((i) => i.status === 'linked' || i.status === 'guest'));
  const pending = $derived(identities.filter((i) => i.status === 'pending'));

  async function load() {
    const admin = session.isAdmin;
    [identities, channels, spaces, users] = await Promise.all([
      api.get<ChannelIdentity[]>('/api/identities').catch(() => []),
      admin ? api.get<ChannelStatus[]>('/api/channels').catch(() => []) : [],
      api.get<Space[]>('/api/spaces').catch(() => []),
      session.me?.user.role === 'owner' && session.multi ? api.get<User[]>('/api/users').catch(() => []) : [],
    ]);
  }

  $effect(() => {
    void load();
    const offs = [
      events.on('channel.status', (e) => {
        const s = (e.data as ChannelStatusData).status;
        channels = [...channels.filter((c) => c.name !== s.name), s];
      }),
      events.on('pairing.linked', () => void load()),
    ];
    return () => offs.forEach((f) => f());
  });

  async function test(name: string) {
    try {
      toasts.ok(`Connected as ${(await api.post<ChannelTestResp>(`/api/channels/${name}/test`)).identity}`);
    } catch (e) {
      toasts.error(e);
    }
  }

  // A refused change leaves the bound value as it was, so Svelte would not
  // reset the control the user just flipped; put it back by hand.
  async function patchSpace(s: Space, body: Record<string, unknown>, el: HTMLInputElement | HTMLSelectElement, was: boolean | string) {
    try {
      Object.assign(s, await api.patch<Space>(`/api/spaces/${encodeURIComponent(s.principal_id)}`, body));
    } catch (e) {
      if (el instanceof HTMLInputElement) el.checked = was as boolean;
      else el.value = was as string;
      toasts.error(e);
    }
  }

  async function setChannel(n: string, box: HTMLInputElement) {
    const path = `channels.${n}.enabled`;
    if (await config.set({ [path]: box.checked })) box.checked = config.flag(path);
  }

  const idPath = (i: ChannelIdentity) => `/api/identities/${encodeURIComponent(i.channel)}/${encodeURIComponent(i.external_id)}`;

  // Linking gives the chat account that person's data. Only the owner may link
  // it to someone else and must pick them explicitly; other admins link to
  // themselves, and everyone else pairs their own account with a code.
  const linkTarget = (i: ChannelIdentity) => (users.length ? linkTo[i.external_id] : session.me?.user.id);

  async function decide(i: ChannelIdentity, status: IdentityStatus) {
    const user_id = linkTarget(i);
    if (status === 'linked' && !user_id) return;
    const body = status === 'linked' ? { status, user_id } : { status };
    if (await attempt(() => api.patch(idPath(i), body), status === 'blocked' ? 'Blocked.' : status === 'guest' ? 'Added as a guest with read-only tools.' : 'Linked.')) void load();
  }

  async function unlink(i: ChannelIdentity) {
    if (await attempt(() => api.del(idPath(i)), 'Unlinked.')) identities = identities.filter((x) => x !== i);
  }
</script>

{#if session.isAdmin}
  <Section title="Connections" desc="Bothy talks to you through these. Agent messages never carry link previews.">
    {#each names as n (n)}
      {@const st = channels.find((c) => c.name === n)}
      <div class="py-3.5">
        <div class="flex flex-wrap items-center gap-3">
          <span class="grid size-9 place-items-center rounded-lg bg-sunken"><Icon name={originIcon[n]} /></span>
          <div class="min-w-0 flex-1">
            <p class="font-medium">{channelName[n]} {#if st?.identity}<span class="text-sm font-normal text-muted">{st.identity}</span>{/if}</p>
            <p class="text-xs text-muted">{st?.detail || (st ? `${st.state} · since ${ago(st.since)}` : 'not configured')}</p>
          </div>
          {#if st}<span class="chip {st.state === 'connected' ? 'chip-ok' : st.state === 'auth_failed' ? 'chip-bad' : st.state === 'disabled' ? '' : 'chip-warn'}">{st.state.replace('_', ' ')}</span>{/if}
          <input type="checkbox" class="switch" aria-label="Enable {channelName[n]}" checked={config.flag(`channels.${n}.enabled`)} onchange={(e) => setChannel(n, e.currentTarget)} />
          {#if st?.enabled}<button type="button" class="btn btn-sm" onclick={() => test(n)}>Test</button>{/if}
          <button type="button" class="btn btn-sm" aria-expanded={open === n} onclick={() => (open = open === n ? '' : n)}>{open === n ? 'Close' : 'Set up'}</button>
        </div>
        {#if open === n}<div class="mt-4 rounded-xl bg-sunken/60 p-4"><ChannelSetup name={n} /></div>{/if}
      </div>
    {/each}
    <ConfigField path="channels.unknown_senders" label="Strangers who message the bot" options={[['pair', 'Reply once with a code you can approve'], ['ignore', 'Ignore silently']]} />
  </Section>
{/if}

<Section title="Link your accounts" desc="Pair your own chat app account so Bothy recognises you there.">
  <div class="py-4"><Pairing /></div>
  {#each linked as i (i.channel + i.external_id)}
    <div class="flex items-center gap-3 py-3 text-sm">
      <Icon name={originIcon[i.channel] ?? 'chat'} size={16} class="text-muted" />
      <span class="min-w-0 flex-1">{i.display_name || i.username} <span class="text-muted">· {channelName[i.channel] ?? i.channel}{i.status === 'guest' ? ' · guest' : ''}</span></span>
      <span class="text-xs text-faint">seen {ago(i.last_seen_at)}</span>
      <button type="button" class="btn btn-ghost btn-sm" onclick={() => unlink(i)}>Unlink</button>
    </div>
  {/each}
</Section>

{#if session.isAdmin && pending.length}
  <Section title="Waiting for approval" desc="People who messaged the bot. Nothing reaches the model until you decide.">
    {#each pending as i (i.channel + i.external_id)}
      <div class="flex flex-wrap items-center gap-2 py-3 text-sm">
        <span class="min-w-0 flex-1">{i.display_name || i.username} <span class="text-muted">· {channelName[i.channel] ?? i.channel} · {ago(i.first_seen_at)}</span></span>
        <button type="button" class="btn btn-sm" onclick={() => decide(i, 'guest')}>Allow as guest</button>
        {#if users.length}
          <select class="input h-7 w-auto text-xs" aria-label="Link to user" value={linkTo[i.external_id] ?? ''} onchange={(e) => (linkTo[i.external_id] = e.currentTarget.value)}>
            <option value="">Link to…</option>
            {#each users as u (u.id)}<option value={u.id}>{u.display_name || u.username}</option>{/each}
          </select>
        {/if}
        <button type="button" class="btn btn-sm" disabled={!linkTarget(i)} onclick={() => decide(i, 'linked')}>Link</button>
        <button type="button" class="btn btn-sm btn-ghost text-bad" onclick={() => decide(i, 'blocked')}>Block</button>
      </div>
    {/each}
  </Section>
{/if}

{#if spaces.length}
  <Section title="Group chats" desc="Groups get no memory or vault and only read-only tools unless you allow more; approvals go to your DM.">
    {#each spaces as s (s.principal_id)}
      <div class="flex flex-wrap items-center gap-x-4 gap-y-2 py-3.5 text-sm">
        <div class="min-w-0 flex-1">
          <p class="font-medium">{s.title}</p>
          <p class="text-xs text-muted">{channelName[s.chat.channel] ?? s.chat.channel} · {s.kind}</p>
        </div>
        <label class="flex items-center gap-2"><input type="checkbox" class="switch" checked={s.enabled} onchange={(e) => patchSpace(s, { enabled: e.currentTarget.checked }, e.currentTarget, s.enabled)} />On</label>
        <label class="flex items-center gap-2"><input type="checkbox" class="switch" checked={s.mention_only} onchange={(e) => patchSpace(s, { mention_only: e.currentTarget.checked }, e.currentTarget, s.mention_only)} />Only when mentioned</label>
        {#if session.isAdmin}
          {@const p = session.principals.find((x) => x.id === s.principal_id)}
          <select class="input h-8 w-auto text-xs" aria-label="Tools for {s.title}" value={p?.tool_profile ?? 'restricted'} onchange={(e) => patchSpace(s, { tool_profile: e.currentTarget.value }, e.currentTarget, p?.tool_profile ?? 'restricted')}>
            <option value="restricted">Read-only tools</option>
            <option value="standard">Normal tools (asks you)</option>
          </select>
        {/if}
      </div>
    {/each}
  </Section>
{/if}
