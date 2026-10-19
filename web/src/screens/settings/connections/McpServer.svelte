<script lang="ts">
  import { untrack } from 'svelte';
  import { Button, Dialog, DiffView, Disclosure, CodeBlock, EmptyState, IconButton, LinkButton, Menu, Select, SettingsSection, StatusLine, Switch, toast } from '../../../components/ui';
  import { api, ApiError, errorText, refusal } from '../../../lib/api/client';
  import type { MCPPin, MCPStatus, SideEffect } from '../../../lib/api/types.gen';
  import { config } from '../../../lib/config.svelte';
  import { lineDiff } from '../../../lib/diff';
  import { plural } from '../../../lib/format';
  import { router } from '../../../lib/router.svelte';
  import ConnPage from './ConnPage.svelte';
  import { effects, saveConfig, serverTitle, toolProblem, type Failure } from './connections';
  import Loading from './Loading.svelte';

  let { params }: { params: Record<string, string> } = $props();
  const name = $derived(params.item.slice('mcp-'.length));
  const title = $derived(serverTitle(name));
  const enc = $derived(encodeURIComponent(name));

  let status = $state<MCPStatus | undefined>();
  let pins = $state<MCPPin[]>([]);
  let view = $state<'loading' | 'ready' | 'missing' | 'failed'>('loading');
  let problem = $state<Failure | null>(null);
  let reviewing = $state(false);
  let drafts = $state<Record<string, { effect: SideEffect; on: boolean }>>({});
  let shown = $state<Record<string, boolean>>({});
  let removing = $state(false);
  let busy = $state(false);
  let enabled = $state(true);
  let saving = $state(false);

  async function load() {
    try {
      const [all, p] = await Promise.all([api.get<MCPStatus[]>('/api/mcp'), api.get<MCPPin[]>(`/api/mcp/${enc}/pins`).catch(() => []), config.load(true)]);
      status = all.find((s) => s.name === name);
      pins = p;
      enabled = config.value(`mcp.servers.${name}.enabled`, null) !== false;
      view = status || config.keys('mcp.servers').includes(name) ? 'ready' : 'missing';
    } catch {
      view = 'failed';
    }
  }

  $effect(() => {
    void name;
    untrack(() => {
      view = 'loading';
      void load().then(() => router.query.get('review') === '1' && pending.length && openReview());
    });
  });

  const off = $derived(status ? status.state === 'disabled' && !status.code : !enabled);
  const pending = $derived(pins.filter((p) => p.status === 'pending'));
  // An action whose change waits for review is decided in the review, not here.
  const actions = $derived(pins.filter((p) => (p.status === 'approved' || p.status === 'disabled') && !pending.some((q) => q.tool === p.tool)));
  const def = (p: MCPPin) => (p.definition ?? {}) as { title?: string; description?: string; inputSchema?: unknown };
  const label = (p: MCPPin) => def(p).title || serverTitle(p.tool);
  const known = (e: SideEffect) => (effects.some((x) => x.value === e) ? e : 'irreversible');
  const pretty = (p?: MCPPin) => (p ? JSON.stringify(p.definition ?? {}, null, 2) : '');
  const before = (p: MCPPin) => pins.find((x) => x.tool === p.tool && x.status === 'approved') ?? pins.find((x) => x.tool === p.tool && x.status !== 'pending');
  // A server just added has only new actions: nothing "changed".
  const allNew = $derived(pending.length > 0 && pending.every((p) => !before(p)));

  async function approve(tools: Record<string, unknown>, done?: string): Promise<boolean> {
    problem = null;
    try {
      await api.post(`/api/mcp/${enc}/approve`, { tools });
      if (done) toast(done);
      return true;
    } catch (e) {
      problem = { message: (e instanceof ApiError && e.status === 409 && refusal(e)) || "Couldn't save. Try again.", details: errorText(e) };
      return false;
    } finally {
      await load();
    }
  }

  // Hash pins the reviewed definition: the server refuses it if the action changed since.
  const decision = (p: MCPPin, effect: SideEffect, on: boolean) =>
    on ? { side_effect: effect, private: p.private, field_map: p.field_map, hash: p.hash } : { side_effect: effect, private: false, disable: true };

  function openReview() {
    drafts = Object.fromEntries(pending.map((p) => [p.tool, { effect: known(p.side_effect || 'external_send'), on: true }]));
    shown = {};
    reviewing = true;
  }

  async function saveReview() {
    busy = true;
    const tools = Object.fromEntries(pending.map((p) => [p.tool, decision(p, drafts[p.tool].effect, drafts[p.tool].on)]));
    if (await approve(tools, 'Review saved.')) reviewing = false;
    busy = false;
  }

  async function setEnabled(v: boolean) {
    saving = true;
    problem = await saveConfig({ [`mcp.servers.${name}.enabled`]: v });
    saving = false;
    if (problem) enabled = !v;
    await load();
  }

  // The key the Add dialog saved for this server goes with it.
  const ownKey = $derived(config.value(`mcp.servers.${name}.headers.Authorization`, '') === `secret:mcp_${name}_header`);

  async function remove() {
    busy = true;
    const key = ownKey;
    problem = await saveConfig({ [`mcp.servers.${name}`]: null });
    if (!problem && key) await api.del(`/api/vault/global/mcp_${name}_header`).catch(() => {});
    busy = false;
    removing = false;
    if (problem) return;
    toast('Server removed.');
    router.navigate('/settings/connections');
  }

  // The fix the status line offers for what its code says.
  const fix = $derived.by(() => {
    switch (status?.code) {
      case 'private_address':
        return { label: `Allow ${hostOf(config.value(`mcp.servers.${name}.url`, ''))}`, onclick: allowPrivate };
      case 'not_installed':
        return { label: 'Install', onclick: install };
      case 'key_missing':
      case 'key_locked':
      case 'key_rejected':
        return { label: 'Open saved keys', onclick: () => router.navigate('/settings/security/keys') };
    }
    return status?.state === 'error' ? { label: 'Try again', onclick: reload } : undefined;
  });
  const hostOf = (u: string) => {
    try {
      return new URL(u).hostname;
    } catch {
      return 'this address';
    }
  };

  async function allowPrivate() {
    problem = await saveConfig({ [`mcp.servers.${name}.allow_private`]: true });
    await load();
  }

  async function install() {
    try {
      await api.post(`/api/mcp/${enc}/install`);
      toast('Install started.');
      await load();
    } catch (e) {
      problem = { message: "Bothy couldn't start the install. Try again.", details: errorText(e) };
    }
  }

  async function reload() {
    try {
      await api.post(`/api/mcp/${enc}/reload`);
      toast('Reload started.');
      await load();
    } catch (e) {
      problem = { message: "Bothy couldn't reload this server. Try again.", details: errorText(e) };
    }
  }
</script>

{#snippet menu()}
  {#if view === 'ready'}
    <Menu
      placement="bottom-end"
      items={[
        { label: 'Reload', onselect: reload },
        { label: 'Remove server…', danger: true, onselect: () => (removing = true) },
      ]}
    >
      {#snippet trigger(props)}<IconButton icon="more" label="Options for {title}" {...props} />{/snippet}
    </Menu>
  {/if}
{/snippet}

<ConnPage title={view === 'missing' ? 'Tool not found' : title} actions={menu}>
  {#if view === 'loading'}
    <Loading />
  {:else if view === 'failed'}
    <EmptyState text="This page didn't load. Check your connection and try again." action={{ label: 'Try again', onclick: load, variant: 'secondary' }} />
  {:else if view === 'missing'}
    <EmptyState text="This tool was removed or never added." action={{ label: 'Go to Connections', href: '/settings/connections' }} />
  {:else}
    <!-- The switch sits on the status line: one place says whether Bothy uses this server. -->
    <div class="flex items-start gap-4">
      <div class="min-w-0 flex-1 space-y-3">
        {#if off}
          <p class="text-body text-muted">Off</p>
        {:else if !status || status.state === 'connecting' || status.state === 'installing'}
          <StatusLine tone="progress" text={status?.state === 'installing' ? 'Installing…' : 'Connecting…'} />
        {:else if status.state === 'error' || status.state === 'no_sandbox' || status.state === 'disabled'}
          <StatusLine tone="danger" text="Can't start: {toolProblem(status)}." details={status.error} action={fix} />
        {:else if pending.length}
          <StatusLine
            tone="warning"
            text={allNew
              ? pending.length === 1
                ? '1 new action needs a review before Bothy can use it.'
                : `${plural(pending.length, 'new action')} need a review before Bothy can use them.`
              : pending.length === 1
                ? '1 action changed and needs a review before Bothy can use it.'
                : `${plural(pending.length, 'action')} changed and need a review before Bothy can use them.`}
          />
          <Button onclick={openReview}>{allNew ? 'Review actions' : 'Review changes'}</Button>
        {:else}
          <p class="text-body text-muted">Ready · {plural(status.tools, 'action')}</p>
        {/if}
        {#if problem}<StatusLine tone="danger" alert text={problem.message} details={problem.details} />{/if}
      </div>
      <div class="flex min-h-6 shrink-0 items-center gap-2">
        <span class="text-small text-muted" aria-live="polite">{saving ? 'Saving…' : ''}</span>
        <Switch label="Turn on {title}" hideLabel bind:checked={enabled} onchange={setEnabled} />
      </div>
    </div>

    <SettingsSection title="Actions">
      {#each actions as p (p.tool)}
        <div class="flex flex-wrap items-start gap-x-6 gap-y-2 border-b border-border py-3 last:border-b-0">
          <div class="min-w-0 flex-1 basis-64">
            <p class="text-body font-medium text-text">{label(p)}</p>
            {#if def(p).description}<p class="text-small text-muted" data-copy-ignore>{def(p).description}</p>{/if}
            <Disclosure class="mt-1">
              <CodeBlock code={JSON.stringify(def(p).inputSchema ?? p.definition ?? {}, null, 2)} maxLines={12} />
            </Disclosure>
          </div>
          <div class="flex items-center gap-3 max-sm:w-full">
            <Select
              class="w-56 max-sm:flex-1"
              label="What {label(p).toLowerCase()} does"
              hideLabel
              value={known(p.side_effect)}
              options={effects}
              onchange={(e) => approve({ [p.tool]: decision(p, e.currentTarget.value as SideEffect, p.status === 'approved') })}
            />
            <Switch
              label="Bothy can {label(p).toLowerCase()}"
              hideLabel
              checked={p.status === 'approved'}
              onchange={(v) => approve({ [p.tool]: decision(p, known(p.side_effect), v) })}
            />
          </div>
        </div>
      {:else}
        <p class="text-body text-muted">
          {off ? 'Turn it on to see what it can do.' : pending.length ? 'Review the changes above to use its actions.' : 'This server has no actions yet.'}
        </p>
      {/each}
    </SettingsSection>
  {/if}
</ConnPage>

<Dialog bind:open={reviewing} title={allNew ? `Review actions from ${title}` : `Review changes to ${title}`} size="lg" {busy}>
  <div class="space-y-8">
    {#each pending as p (p.tool)}
      {@const d = drafts[p.tool]}
      {#if d}
        <section aria-label={label(p)}>
          <h3 class="text-heading text-text">{label(p)}</h3>
          <p class="text-small text-muted">{before(p) ? 'Changed since you last allowed it' : 'New action'}</p>
          {#if def(p).description}<p class="mt-1 text-body text-text" data-copy-ignore>{def(p).description}</p>{/if}
          {#if before(p)}
            <p class="mt-1 text-small">
              <LinkButton aria-expanded={!!shown[p.tool]} onclick={() => (shown[p.tool] = !shown[p.tool])}>{shown[p.tool] ? 'Hide what changed' : 'Show what changed'}</LinkButton>
            </p>
            {#if shown[p.tool]}<DiffView class="mt-2" diff={lineDiff(pretty(before(p)), pretty(p))} maxLines={40} />{/if}
          {:else}
            <!-- A new action has nothing to compare; its definition stays one click away. -->
            <Disclosure class="mt-1">
              <CodeBlock code={JSON.stringify(def(p).inputSchema ?? p.definition ?? {}, null, 2)} maxLines={12} />
            </Disclosure>
          {/if}
          <div class="mt-3 flex flex-wrap items-end justify-between gap-x-6 gap-y-3">
            <Select class="w-full sm:w-72" label="What this does" bind:value={d.effect} options={effects} />
            <Switch label="Allow this action" bind:checked={d.on} />
          </div>
        </section>
      {/if}
    {/each}
  </div>
  {#snippet footer({ close })}
    <Button data-cancel onclick={close}>Cancel</Button>
    <Button variant="primary" loading={busy} loadingLabel="Saving…" onclick={saveReview}>Save <span class="max-sm:hidden">review</span></Button>
  {/snippet}
</Dialog>

<Dialog bind:open={removing} title="Remove {title}?" initialFocus="cancel" {busy}>
  <p class="text-body text-text">Bothy loses these actions{ownKey ? ' and deletes its saved key' : ''}. You can add the server again later.</p>
  {#snippet footer({ close })}
    <Button data-cancel onclick={close}>Cancel</Button>
    <Button variant="danger" loading={busy} loadingLabel="Removing…" onclick={remove}>Remove server</Button>
  {/snippet}
</Dialog>
