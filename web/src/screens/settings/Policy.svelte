<script lang="ts">
  import ApprovalCard from '../../components/ApprovalCard.svelte';
  import ConfigField from '../../components/ConfigField.svelte';
  import Dialog from '../../components/Dialog.svelte';
  import Icon from '../../components/Icon.svelte';
  import PresetPicker from '../../components/PresetPicker.svelte';
  import Section from '../../components/Section.svelte';
  import SourceChip from '../../components/SourceChip.svelte';
  import Verbatim from '../../components/Verbatim.svelte';
  import { api, errorText } from '../../lib/api/client';
  import type { Approval, Grant, PolicyPreviewResp, SideEffect } from '../../lib/api/types.gen';
  import { config } from '../../lib/config.svelte';
  import { ago, isZeroTime } from '../../lib/format';
  import { risk, sideEffects } from '../../lib/labels';
  import { attempt } from '../../lib/stores/ui.svelte';

  let grants = $state<Grant[]>([]);
  let editing = $state<Record<string, string> | null>(null);
  let test = $state({ tool: 'shell', args: '{"command": "rm -rf build/"}', trigger: 'message' });
  let result = $state<PolicyPreviewResp | null>(null);
  let testError = $state('');

  const rules = $derived(config.keys('policy.rules'));
  const rv = (id: string, f: string) => config.value<unknown>(`policy.rules.${id}.${f}`);
  const list = (v: unknown) => (Array.isArray(v) ? v.join(', ') : '');

  $effect(() => {
    api.get<Grant[]>('/api/grants').then((g) => (grants = g), () => {});
  });

  function edit(id = '') {
    editing = {
      id,
      tool: id ? String(rv(id, 'tool') ?? '') : '',
      argv0: id ? list(rv(id, 'argv0')) : '',
      domains: id ? list(rv(id, 'domains')) : '',
      paths: id ? list(rv(id, 'paths')) : '',
      verdict: id ? String(rv(id, 'verdict') ?? 'ask') : 'ask',
      reason: id ? String(rv(id, 'reason') ?? '') : '',
      orig: id,
    };
  }

  async function saveRule(e: SubmitEvent) {
    e.preventDefault();
    const r = editing!;
    const split = (s: string) => s.split(',').map((x) => x.trim()).filter(Boolean);
    const set: Record<string, unknown> = {
      [`policy.rules.${r.id}`]: { id: r.id, tool: r.tool, argv0: split(r.argv0), domains: split(r.domains), paths: split(r.paths), verdict: r.verdict, reason: r.reason },
    };
    if (r.orig && r.orig !== r.id) set[`policy.rules.${r.orig}`] = null;
    if (!(await config.set(set))) editing = null;
  }

  async function runTest(e: SubmitEvent) {
    e.preventDefault();
    testError = '';
    result = null;
    try {
      result = await api.post<PolicyPreviewResp>('/api/policy/preview', { tool: test.tool, args: JSON.parse(test.args || '{}'), trigger: test.trigger });
    } catch (err) {
      testError = err instanceof SyntaxError ? 'Arguments must be a JSON object.' : errorText(err);
    }
  }

  const previewApproval = (r: PolicyPreviewResp): Approval => ({
    id: 'preview',
    run_id: '',
    session_id: '',
    principal_id: '',
    tool_call_id: '',
    action: r.action,
    decision: r.decision,
    card: r.card!,
    status: 'pending',
    approver: r.decision.approver,
    on_timeout: r.decision.on_timeout,
    created_at: '',
    expires_at: '',
  });

  async function revoke(g: Grant) {
    if (await attempt(() => api.del(`/api/grants/${encodeURIComponent(g.id)}`), 'Revoked. Bothy will ask again next time.')) grants = grants.filter((x) => x.id !== g.id);
  }

  const grantText = (g: Grant) =>
    g.kind === 'tool' ? `${g.tool}${g.pattern ? ' where ' + Object.entries(g.pattern).map(([k, v]) => `${k} ~ ${v}`).join(', ') : ''}` : `${g.kind === 'domain' ? 'Connect to' : 'Send to'} ${g.value}`;
</script>

<Section title="How cautious should Bothy be?" desc="Safety floors (the lethal trifecta, money, admin changes) always ask, whatever you pick.">
  <div class="py-4">
    <div class="mb-3"><SourceChip entry={config.entry('policy.preset')} /></div>
    <PresetPicker />
  </div>
</Section>

<Section title="Per-class overrides" desc="Change what the preset does for one kind of action.">
  {#each sideEffects as c (c)}
    <ConfigField path="policy.defaults.{c}" label={risk[c].label} type="select" options={[['', 'As the preset says'], ['auto', 'Allow'], ['ask', 'Ask'], ['deny', 'Deny']]} />
  {/each}
</Section>

<Section title="Rules" desc="First match wins. Rules can never override a safety floor.">
  {#snippet actions()}<button type="button" class="btn btn-sm" onclick={() => edit()}><Icon name="plus" size={14} />Add rule</button>{/snippet}
  {#each rules as id (id)}
    {@const v = String(rv(id, 'verdict'))}
    <div class="flex flex-wrap items-center gap-3 py-3 text-sm">
      <span class="chip {v === 'auto' ? 'chip-ok' : v === 'deny' ? 'chip-bad' : 'chip-warn'}">{v === 'auto' ? 'allow' : v}</span>
      <div class="min-w-0 flex-1">
        <p class="font-medium">{id} <span class="mono font-normal text-muted">{rv(id, 'tool')}</span></p>
        <p class="truncate text-xs text-muted">
          {[list(rv(id, 'argv0')) && `commands: ${list(rv(id, 'argv0'))}`, list(rv(id, 'domains')) && `sites: ${list(rv(id, 'domains'))}`, list(rv(id, 'paths')) && `paths: ${list(rv(id, 'paths'))}`, rv(id, 'reason')].filter(Boolean).join(' · ')}
        </p>
      </div>
      <SourceChip entry={config.entry(`policy.rules.${id}.tool`)} />
      <button type="button" class="btn btn-ghost btn-sm" onclick={() => edit(id)}>Edit</button>
    </div>
  {:else}
    <p class="py-4 text-sm text-muted">No rules: the preset decides.</p>
  {/each}
</Section>

<Section title="What would happen?" desc="Plans a sample tool call and shows the decision and card, without running anything.">
  <form class="grid gap-3 py-4" onsubmit={runTest}>
    <div class="grid gap-3 sm:grid-cols-[10rem_1fr_9rem]">
      <input class="input mono" aria-label="Tool" placeholder="shell" bind:value={test.tool} />
      <input class="input mono" aria-label="Arguments (JSON)" bind:value={test.args} />
      <select class="input" aria-label="Trigger" bind:value={test.trigger}>
        <option value="message">Chat message</option>
        <option value="schedule">Schedule</option>
        <option value="api">API</option>
      </select>
    </div>
    <button class="btn justify-self-start">Check</button>
  </form>
  {#if testError}<p class="alert-bad mb-4" role="alert">{testError}</p>{/if}
  {#if result}
    <div class="space-y-3 pb-4">
      <p class="flex flex-wrap items-center gap-2 text-sm">
        <span class="chip {result.decision.verdict === 'auto' ? 'chip-ok' : result.decision.verdict === 'deny' ? 'chip-bad' : 'chip-warn'}">{result.decision.verdict === 'auto' ? 'runs without asking' : result.decision.verdict === 'ask' ? 'asks first' : 'denied'}</span>
        <code class="text-xs">{result.decision.rule}</code><span class="text-muted">{result.decision.reason}</span>
      </p>
      {#if result.card}<ApprovalCard approval={previewApproval(result)} preview />{/if}
      {#if result.action}<pre class="mono max-h-48 overflow-auto rounded-lg bg-sunken p-3 whitespace-pre-wrap break-all"><Verbatim text={JSON.stringify(result.action, null, 2)} /></pre>{/if}
    </div>
  {/if}
</Section>

<Section title="Standing permissions" desc="“Always allow” answers and admin grants. Revoke to be asked again.">
  {#each grants as g (g.id)}
    <div class="flex flex-wrap items-center gap-3 py-3 text-sm">
      <span class="min-w-0 flex-1 truncate">{grantText(g)}</span>
      <span class="rounded-full border px-2 py-0.5 text-xs {risk[g.max_se as SideEffect]?.cls ?? ''}">{risk[g.max_se as SideEffect]?.label ?? g.max_se}</span>
      <span class="text-xs text-muted">{g.scope} · used {g.uses}× · {isZeroTime(g.expires_at) ? 'no expiry' : `expires ${ago(g.expires_at)}`}</span>
      <button type="button" class="btn btn-ghost btn-sm text-bad" onclick={() => revoke(g)}>Revoke</button>
    </div>
  {:else}
    <p class="py-4 text-sm text-muted">None yet.</p>
  {/each}
</Section>

<Section title="Approval timeouts">
  <ConfigField path="policy.approval.interactive_timeout" label="Chat approvals wait" />
  <ConfigField path="policy.approval.interactive_on_timeout" label="When nobody answers a chat approval" options={[['deny', 'Deny'], ['park', 'Park it']]} />
  <ConfigField path="policy.approval.scheduled_timeout" label="Scheduled approvals wait" />
  <ConfigField path="policy.approval.scheduled_on_timeout" label="When nobody answers a scheduled approval" options={[['park', 'Park it'], ['deny', 'Deny']]} />
  <ConfigField path="policy.approval.park_ttl" label="Parked approvals expire after" />
  <ConfigField path="policy.approval.notify" label="Also send cards to" help="Extra chat apps for cards from web and scheduled runs, e.g. telegram." />
</Section>

<Dialog open={!!editing} title={editing?.orig ? `Edit rule ${editing.orig}` : 'New rule'} onclose={() => (editing = null)}>
  {#if editing}
    <form id="rule-form" class="grid gap-3" onsubmit={saveRule}>
      <div class="grid grid-cols-2 gap-3">
        <div><label class="label" for="r-id">Name</label><input id="r-id" class="input mono" required pattern="[a-z0-9][a-z0-9-]*" bind:value={editing.id} /></div>
        <div><label class="label" for="r-tool">Tool (glob)</label><input id="r-tool" class="input mono" required placeholder="shell" bind:value={editing.tool} /></div>
      </div>
      <div><label class="label" for="r-argv">First word of the command</label><input id="r-argv" class="input mono" placeholder="ls, cat, grep" bind:value={editing.argv0} /></div>
      <div><label class="label" for="r-dom">Sites</label><input id="r-dom" class="input mono" placeholder="*.wikipedia.org" bind:value={editing.domains} /></div>
      <div><label class="label" for="r-path">Workspace paths</label><input id="r-path" class="input mono" placeholder="notes/**" bind:value={editing.paths} /></div>
      <div class="grid grid-cols-2 gap-3">
        <div>
          <label class="label" for="r-v">Then</label>
          <select id="r-v" class="input" bind:value={editing.verdict}><option value="auto">Allow</option><option value="ask">Ask</option><option value="deny">Deny</option></select>
        </div>
        <div><label class="label" for="r-why">Reason shown</label><input id="r-why" class="input" bind:value={editing.reason} /></div>
      </div>
    </form>
  {/if}
  {#snippet footer()}
    {#if editing?.orig}<button type="button" class="btn btn-ghost mr-auto text-bad" onclick={async () => !(await config.set({ [`policy.rules.${editing!.orig}`]: null })) && (editing = null)}>Delete</button>{/if}
    <button type="button" class="btn" onclick={() => (editing = null)}>Cancel</button>
    <button class="btn btn-primary" form="rule-form">Save rule</button>
  {/snippet}
</Dialog>
