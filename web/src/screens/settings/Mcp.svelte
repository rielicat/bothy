<script lang="ts">
  import DiffView from '../../components/DiffView.svelte';
  import Dialog from '../../components/Dialog.svelte';
  import Icon from '../../components/Icon.svelte';
  import Section from '../../components/Section.svelte';
  import SourceChip from '../../components/SourceChip.svelte';
  import { api } from '../../lib/api/client';
  import type { MCPPin, MCPPinData, MCPStatus, SideEffect } from '../../lib/api/types.gen';
  import { config } from '../../lib/config.svelte';
  import { lineDiff } from '../../lib/diff';
  import { risk, sideEffects } from '../../lib/labels';
  import { events } from '../../lib/sse.svelte';
  import { attempt } from '../../lib/stores/ui.svelte';

  type Draft = { side_effect: SideEffect; private: boolean; recipient: string; url: string };

  let servers = $state<MCPStatus[]>([]);
  let review = $state<{ server: string; pins: MCPPin[]; drafts: Record<string, Draft> } | null>(null);
  let form = $state<Record<string, string> | null>(null);

  const names = $derived([...new Set([...config.keys('mcp.servers'), ...servers.map((s) => s.name)])]);
  const cv = (n: string, f: string) => config.value<unknown>(`mcp.servers.${n}.${f}`);

  async function load() {
    servers = await api.get<MCPStatus[]>('/api/mcp').catch(() => []);
  }

  $effect(() => {
    void load();
    return events.on('mcp.pin_pending', (e) => {
      void load();
      if (review?.server === (e.data as MCPPinData).server) void openReview(review.server);
    });
  });

  async function openReview(server: string) {
    const pins = await api.get<MCPPin[]>(`/api/mcp/${encodeURIComponent(server)}/pins`).catch(() => [] as MCPPin[]);
    const drafts: Record<string, Draft> = {};
    for (const p of pins.filter((x) => x.status === 'pending')) drafts[p.tool] = { side_effect: p.side_effect || 'external_send', private: p.private, recipient: p.field_map?.recipient ?? '', url: p.field_map?.url ?? '' };
    review = { server, pins, drafts };
  }

  const previous = (p: MCPPin) => review?.pins.find((x) => x.tool === p.tool && x.status !== 'pending');
  const pretty = (v: unknown) => JSON.stringify(v ?? {}, null, 2);

  async function decide(tool: string, disable = false) {
    const d = review!.drafts[tool];
    const field_map = Object.fromEntries(Object.entries({ recipient: d.recipient, url: d.url }).filter(([, v]) => v));
    // The reviewed definition's hash: the server refuses the approval if the
    // tool changed after this review was opened.
    const hash = review!.pins.find((x) => x.tool === tool && x.status === 'pending')?.hash;
    const body = { tools: { [tool]: disable ? { side_effect: d.side_effect, private: false, disable: true } : { side_effect: d.side_effect, private: d.private, field_map, hash } } };
    if (await attempt(() => api.post(`/api/mcp/${encodeURIComponent(review!.server)}/approve`, body), disable ? `${tool} disabled.` : `${tool} approved.`)) {
      delete review!.drafts[tool];
      void load();
    }
  }

  function edit(n = '') {
    const pkg = (f: string) => String(cv(n, `package.${f}`) ?? '');
    form = {
      orig: n,
      name: n,
      transport: n ? String(cv(n, 'transport') ?? 'http') : 'http',
      url: n ? String(cv(n, 'url') ?? '') : '',
      auth: n ? String(cv(n, 'headers.Authorization') ?? '') : '',
      ecosystem: pkg('ecosystem') || 'npm',
      pkg: pkg('name'),
      version: pkg('version'),
      integrity: pkg('integrity'),
      command: n ? ((cv(n, 'command') as string[] | undefined) ?? []).join(' ') : '',
      egress: n ? ((cv(n, 'egress') as string[] | undefined) ?? []).join(', ') : '',
      workspace: n ? String(cv(n, 'workspace') || 'none') : 'none',
      side: n ? String(cv(n, 'side_effects.default') ?? 'external_send') : 'external_send',
    };
  }

  const install = $derived(
    form?.transport === 'stdio' && form.pkg
      ? form.ecosystem === 'npm'
        ? `npm install --ignore-scripts --no-audit --no-fund --save-exact --prefix /opt/mcp ${form.pkg}@${form.version}`
        : `uv pip compile --generate-hashes (${form.pkg}==${form.version}) && uv pip install --no-deps --require-hashes -r lock.txt --target /opt/mcp`
      : '',
  );

  async function save(e: SubmitEvent) {
    e.preventDefault();
    const f = form!;
    const split = (s: string, re: RegExp) => s.split(re).map((x) => x.trim()).filter(Boolean);
    const server: Record<string, unknown> = { name: f.name, transport: f.transport, side_effects: { default: f.side } };
    if (f.transport === 'http') Object.assign(server, { url: f.url, headers: f.auth ? { Authorization: f.auth } : {} });
    else Object.assign(server, { package: { ecosystem: f.ecosystem, name: f.pkg, version: f.version, integrity: f.integrity }, command: split(f.command, /\s+/), egress: split(f.egress, /,/), workspace: f.workspace });
    const set: Record<string, unknown> = { [`mcp.servers.${f.name}`]: server };
    if (f.orig && f.orig !== f.name) set[`mcp.servers.${f.orig}`] = null;
    if (!(await config.set(set))) {
      form = null;
      void load();
    }
  }
</script>

<Section title="MCP servers" desc="Remote (HTTP) servers get vault-injected headers; local (stdio) servers run in a sandbox box with no secrets.">
  {#snippet actions()}<button type="button" class="btn btn-sm" onclick={() => edit()}><Icon name="plus" size={14} />Add server</button>{/snippet}
  {#each names as n (n)}
    {@const s = servers.find((x) => x.name === n)}
    <div class="flex flex-wrap items-center gap-3 py-3.5">
      <span class="grid size-9 place-items-center rounded-lg bg-sunken"><Icon name="plug" /></span>
      <div class="min-w-0 flex-1">
        <p class="font-medium">{n} <span class="text-xs font-normal text-muted">{s?.transport ?? cv(n, 'transport')}{s?.protocol ? ` · ${s.protocol}` : ''}{s ? ` · ${s.tools} tools` : ''}</span></p>
        {#if s?.error}<p class="text-xs text-bad">{s.error}</p>{/if}
      </div>
      <SourceChip entry={config.entry(`mcp.servers.${n}.name`)} />
      {#if s}<span class="chip {s.state === 'ready' ? 'chip-ok' : s.state === 'error' ? 'chip-bad' : s.state === 'pins_pending' ? 'chip-warn' : ''}">{s.state.replace('_', ' ')}</span>{/if}
      {#if s?.pending}<button type="button" class="btn btn-sm btn-primary" onclick={() => openReview(n)}>Review {s.pending} tool{s.pending === 1 ? '' : 's'}</button>{/if}
      {#if cv(n, 'transport') === 'stdio'}<button type="button" class="btn btn-sm" onclick={() => attempt(() => api.post(`/api/mcp/${encodeURIComponent(n)}/install`), 'Installing in a sandbox box…')}>Install</button>{/if}
      <button type="button" class="btn btn-sm" onclick={() => attempt(() => api.post(`/api/mcp/${encodeURIComponent(n)}/reload`), 'Reloading…')}>Reload</button>
      <button type="button" class="btn btn-ghost btn-sm" onclick={() => edit(n)}>Edit</button>
    </div>
  {:else}
    <p class="py-6 text-center text-sm text-muted">No MCP servers. Add one to give Bothy more tools; every tool needs your review before it can be used.</p>
  {/each}
</Section>

<Dialog open={!!review} title="Review tools from {review?.server}" kind="sheet" onclose={() => (review = null)}>
  {#if review}
    {#each review.pins.filter((p) => p.status === 'pending' && review!.drafts[p.tool]) as p (p.tool)}
      {@const d = review.drafts[p.tool]}
      {@const prev = previous(p)}
      <article class="card mb-4 space-y-3 p-4">
        <h3 class="font-medium">{p.tool} {#if prev}<span class="chip chip-warn">changed</span>{:else}<span class="chip chip-info">new</span>{/if}</h3>
        <DiffView diff={lineDiff(prev ? pretty(prev.definition) : '', pretty(p.definition))} />
        <div class="grid gap-3 sm:grid-cols-2">
          <div>
            <label class="label" for="se-{p.tool}">What it can do</label>
            <select id="se-{p.tool}" class="input" bind:value={d.side_effect}>
              {#each sideEffects as c (c)}<option value={c}>{risk[c].label}</option>{/each}
            </select>
          </div>
          <label class="flex items-center gap-2 self-end pb-2 text-sm"><input type="checkbox" class="switch" bind:checked={d.private} />Output is private data</label>
          <div><label class="label" for="fr-{p.tool}">Recipient field</label><input id="fr-{p.tool}" class="input mono" placeholder="/to" bind:value={d.recipient} /></div>
          <div><label class="label" for="fu-{p.tool}">URL field</label><input id="fu-{p.tool}" class="input mono" placeholder="/url" bind:value={d.url} /></div>
        </div>
        <p class="help">Field pointers let Bothy ask the first time a new recipient or site appears.</p>
        <div class="flex gap-2">
          <button type="button" class="btn btn-primary btn-sm" onclick={() => decide(p.tool)}>Approve</button>
          <button type="button" class="btn btn-sm" onclick={() => decide(p.tool, true)}>Disable</button>
        </div>
      </article>
    {:else}
      <p class="text-sm text-muted">Nothing left to review.</p>
    {/each}
  {/if}
</Dialog>

<Dialog open={!!form} title={form?.orig ? `Edit ${form.orig}` : 'Add an MCP server'} kind="sheet" onclose={() => (form = null)}>
  {#if form}
    <form id="mcp-form" class="grid gap-3" onsubmit={save}>
      <div><label class="label" for="m-name">Name</label><input id="m-name" class="input mono" required pattern="[a-z0-9][a-z0-9-]{'{0,31}'}" bind:value={form.name} /></div>
      <div>
        <label class="label" for="m-tr">Transport</label>
        <select id="m-tr" class="input" bind:value={form.transport}><option value="http">Remote (Streamable HTTP)</option><option value="stdio">Local package in a sandbox (stdio)</option></select>
      </div>
      {#if form.transport === 'http'}
        <div><label class="label" for="m-url">URL</label><input id="m-url" class="input mono" type="url" required placeholder="https://mcp.example.com/mcp" bind:value={form.url} /></div>
        <div><label class="label" for="m-auth">Authorization header</label><input id="m-auth" class="input mono" placeholder="Bearer secret:docs_mcp_token" bind:value={form.auth} /><p class="help">Reference a vault secret; values never appear in config.</p></div>
      {:else}
        <div class="grid grid-cols-[6rem_1fr] gap-3">
          <div><label class="label" for="m-eco">Registry</label><select id="m-eco" class="input" bind:value={form.ecosystem}><option value="npm">npm</option><option value="pypi">PyPI</option></select></div>
          <div><label class="label" for="m-pkg">Package</label><input id="m-pkg" class="input mono" required bind:value={form.pkg} /></div>
        </div>
        <div class="grid grid-cols-2 gap-3">
          <div><label class="label" for="m-ver">Exact version</label><input id="m-ver" class="input mono" required bind:value={form.version} /></div>
          <div><label class="label" for="m-int">Integrity</label><input id="m-int" class="input mono" required placeholder="sha512-…" bind:value={form.integrity} /></div>
        </div>
        <div><label class="label" for="m-cmd">Command</label><input id="m-cmd" class="input mono" placeholder="node node_modules/…/index.js" bind:value={form.command} /></div>
        <div><label class="label" for="m-eg">Network access</label><input id="m-eg" class="input mono" placeholder="api.example.com:443" bind:value={form.egress} /></div>
        <div>
          <label class="label" for="m-ws">Workspace</label>
          <select id="m-ws" class="input" bind:value={form.workspace}><option value="none">No access</option><option value="principal">The caller's workspace</option></select>
        </div>
        {#if install}
          <div><p class="label">Bothy will run, in an isolated box</p><pre class="mono overflow-x-auto rounded-lg bg-sunken p-3">{install}</pre><p class="help">The installer box reaches only the package registry. The top-level integrity is verified and the lockfile recorded on first install.{form.egress ? ` At runtime the server may reach: ${form.egress}.` : ' At runtime it gets no network.'}</p></div>
        {/if}
      {/if}
      <div>
        <label class="label" for="m-se">Default class for its tools</label>
        <select id="m-se" class="input" bind:value={form.side}>{#each sideEffects as c (c)}<option value={c}>{risk[c].label}</option>{/each}</select>
      </div>
    </form>
  {/if}
  {#snippet footer()}
    {#if form?.orig}<button type="button" class="btn btn-ghost mr-auto text-bad" onclick={async () => !(await config.set({ [`mcp.servers.${form!.orig}`]: null })) && (form = null)}>Remove</button>{/if}
    <button type="button" class="btn" onclick={() => (form = null)}>Cancel</button>
    <button class="btn btn-primary" form="mcp-form">Save</button>
  {/snippet}
</Dialog>
