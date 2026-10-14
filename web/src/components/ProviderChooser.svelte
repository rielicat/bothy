<script lang="ts" module>
  export interface Connected {
    id: string;
    name: string;
    main: string;
    quick: string;
  }
</script>

<script lang="ts">
  import { onMount, tick, untrack } from 'svelte';
  import { api, ApiError, errorText } from '../lib/api/client';
  import { plural } from '../lib/format';
  import { failure, failureCode, AWS_HELP, type Failure } from '../lib/providerErrors';
  import {
    bestModel,
    consoleLabel,
    friendlyModel,
    hostPort,
    modelNote,
    modelOptions,
    pickModels,
    provider,
    providerEntry,
    providers,
    regionName,
    regions,
    type DetectionV2,
    type ProbeRequest,
    type ProbeV2,
    type ProviderDef,
  } from '../lib/providers';
  import Icon from './Icon.svelte';
  import Button from './ui/Button.svelte';
  import CodeBlock from './ui/CodeBlock.svelte';
  import Disclosure from './ui/Disclosure.svelte';
  import LinkButton from './ui/LinkButton.svelte';
  import OptionList from './ui/OptionList.svelte';
  import Select from './ui/Select.svelte';
  import StatusLine from './ui/StatusLine.svelte';
  import TextField from './ui/TextField.svelte';
  import type { Option } from './ui/OptionList.svelte';

  // Step 2 of setup (docs/design/ui.md §5.6), and Settings → Models → "Add a
  // provider" (§11.4). Setup saves on Continue; settings saves the provider
  // (not the model slots) on the first successful check and calls onconnected.
  // `exclude` names providers already set up, which Settings manages in its own list.
  let { mode = 'setup', exclude = [], onconnected }: { mode?: 'setup' | 'settings'; exclude?: string[]; onconnected?: (c: Connected) => void } = $props();

  type Source = 'aws' | 'local' | 'env' | 'list';
  interface Row extends Option {
    def: ProviderDef;
    source: Source;
    detection?: DetectionV2;
  }
  interface RowState {
    key: string;
    address: string;
    region: string;
    typedModel: string;
    allowPrivate: boolean;
    status: 'idle' | 'checking' | 'connected' | 'failed';
    checking: string;
    failure: Failure | null;
    rejected: boolean;
    details: string;
    byPerson: boolean;
    models: string[];
    main: string;
    quick: string;
    note: string;
    changing: boolean;
    needsModel: boolean;
  }

  const uid = $props.id();
  const setup = $derived(mode === 'setup');
  const IDLE_MS = 800;

  const listRow = (def: ProviderDef): Row => ({ value: `p:${def.id}`, title: def.name, description: def.line, def, source: 'list' });

  let root: HTMLElement | undefined = $state();
  let detections = $state<DetectionV2[] | null>(null);
  let slow = $state(false);
  let selected = $state<string | undefined>();
  // The provider list is usable while detection runs, so its rows have state from the start.
  let states = $state<Record<string, RowState>>(Object.fromEntries(providers.map((p) => [`p:${p.id}`, blank(listRow(p))])));
  let settled = false;
  let more = $state(false);
  let reveal = $state(false);
  let saving = $state(false);
  let saveError = $state('');
  let saveDetails = $state('');
  // Newer checks and edits bump it; an answer to an older one is dropped.
  let seq = 0;
  let timer: ReturnType<typeof setTimeout> | undefined;

  const localDef = (d: DetectionV2): ProviderDef =>
    d.kind === 'ollama' ? provider('ollama') : /:1234\b/.test(d.base_url) ? provider('lmstudio') : { ...provider('custom'), id: 'local', name: 'The model server', key: undefined };

  // AWS, Ollama, LM Studio, other local servers, then keys in the server's environment (§5.6.1).
  function detectedRows(list: DetectionV2[]): Row[] {
    const rank = (d: DetectionV2) => (d.kind === 'bedrock' ? 0 : d.env_var ? 4 : d.kind === 'ollama' ? 1 : /:1234\b/.test(d.base_url) ? 2 : 3);
    const seen = new Set<string>();
    const rows: Row[] = [];
    for (const d of [...list].sort((a, b) => rank(a) - rank(b))) {
      if (d.kind === 'bedrock') {
        rows.push({ value: 'aws', title: "Use this server's AWS access", description: `Amazon Bedrock in ${regionName(states.aws?.region ?? d.region ?? 'us-east-1')}. No keys needed.`, def: provider('bedrock'), source: 'aws', detection: d });
      } else if (d.env_var) {
        const def = providers.find((p) => p.env === d.env_var);
        if (def && !seen.has(d.env_var)) {
          rows.push({ value: `env:${d.env_var}`, title: `Use the ${def.name} key on this server`, description: "Found in the server's settings. Choosing it saves a copy in Bothy's saved keys.", def, source: 'env', detection: d });
          seen.add(d.env_var);
        }
      } else {
        const def = localDef(d);
        // The same server can answer on two addresses (the Docker host and this machine).
        if (seen.has(def.id)) continue;
        seen.add(def.id);
        const models = d.models ?? [];
        const n = models.length;
        const description =
          def.id === 'ollama'
            ? n > 1
              ? `${n} models ready, including ${friendlyModel(bestModel(models))}.`
              : n
                ? `1 model ready: ${friendlyModel(models[0])}.`
                : 'Running, but no models downloaded yet.'
            : `${plural(n, 'model')} ${def.id === 'lmstudio' ? 'ready' : 'available'}.`;
        const title = def.id === 'ollama' ? 'Ollama on this computer' : def.id === 'lmstudio' ? 'LM Studio on this computer' : 'Model server on this computer';
        rows.push({ value: `local:${def.id}`, title, description, def, source: 'local', detection: d });
      }
    }
    return rows;
  }

  const found = $derived(detectedRows(detections ?? []).filter((r) => !exclude.includes(r.def.id)));
  // A provider found on the server leaves the list, unless it was chosen there before detection finished.
  const hide = $derived(new Set([...exclude, ...found.map((r) => (r.source === 'aws' ? 'bedrock' : r.source === 'local' ? r.def.id : '')).filter((id) => id && `p:${id}` !== selected)]));
  const looking = $derived(detections === null && slow);
  const pad = $derived(setup ? '[&>div>label]:px-4 [&>div>div]:mr-4 [&>div>div]:ml-11' : '');
  const base = $derived(providers.filter((p) => !p.more && !hide.has(p.id)).map(listRow));
  const extra = $derived(providers.filter((p) => p.more && !hide.has(p.id)).map(listRow));
  const rows = $derived([...found, ...base, ...extra]);
  const row = (v: string) => rows.find((r) => r.value === v)!;
  const showMore = $derived(more || extra.some((r) => r.value === selected));
  const current = $derived(selected ? states[selected] : undefined);
  const ready = $derived(current?.status === 'connected' && !!current.main);

  function blank(r: Row): RowState {
    return {
      key: '',
      address: r.detection?.base_url || r.def.base || '',
      region: r.detection?.region ?? 'us-east-1',
      typedModel: '',
      // A server Bothy found itself may be opened; a typed private address needs "Allow" (D15).
      allowPrivate: r.source === 'local',
      status: 'idle',
      checking: '',
      failure: null,
      rejected: false,
      details: '',
      byPerson: false,
      models: r.detection?.models ?? [],
      main: '',
      quick: '',
      note: '',
      changing: false,
      needsModel: false,
    };
  }

  onMount(() => {
    const t = setTimeout(() => (slow = true), 400);
    api.post<DetectionV2[]>('/api/providers/detect').then(
      (d) => (detections = d ?? []),
      () => (detections = []),
    );
    return () => {
      clearTimeout(t);
      clearTimeout(timer);
      seq++;
    };
  });

  // Runs once, when detection settles: the found rows get their state, and a
  // single AWS or local option is chosen and checked at once (§5.6.1), unless
  // the person already chose something while detection ran.
  $effect(() => {
    if (detections === null || settled) return;
    settled = true;
    untrack(() => {
      for (const r of found) states[r.value] ??= blank(r);
      const auto = found.filter((r) => r.source !== 'env');
      if (auto.length === 1 && selected === undefined) void tick().then(() => selected === undefined && select(auto[0].value));
    });
  });

  function stop() {
    seq++;
    clearTimeout(timer);
    for (const s of Object.values(states)) if (s.status === 'checking') s.status = 'idle';
  }

  function select(v: string) {
    stop();
    selected = v;
    const r = row(v);
    const s = states[v];
    if (s.status !== 'idle') return;
    if (r.source !== 'list' || r.def.local || (r.def.custom && s.address.trim()) || (r.def.key && !r.def.custom && s.key.trim().length >= 20)) void check(v);
  }

  function edited(v: string, now = false) {
    const s = states[v];
    stop();
    s.status = 'idle';
    s.failure = null;
    s.main = '';
    s.changing = false;
    const r = row(v);
    const value = r.def.local || r.def.custom ? s.address.trim() : s.key.trim();
    if (now && value) void check(v, { byPerson: true });
    else if (value.length >= (r.def.local || r.def.custom ? 8 : 20)) timer = setTimeout(() => check(v), IDLE_MS);
  }

  // Enter checks a value that isn't checked yet and continues once it is (§5.2).
  function enter(e: KeyboardEvent, v: string) {
    if (e.key !== 'Enter') return;
    e.preventDefault();
    if (states[v].status === 'connected' && setup) void save();
    else edited(v, true);
  }

  const checkingLabel = (r: Row) => (r.def.kind === 'bedrock' ? 'Checking AWS access…' : r.def.key && !r.def.custom ? 'Checking the key…' : 'Checking…');

  function request(r: Row, s: RowState): ProbeRequest {
    const d = r.def;
    const detected = r.detection?.models ?? [];
    if (d.kind === 'bedrock') return { kind: 'bedrock', region: s.region, model: s.main || pickModels(d, detected).main || undefined };
    if (r.source === 'env') return { kind: d.kind, base_url: d.base, env_var: r.detection?.env_var, model: s.main || d.model };
    const address = d.local || d.custom || r.source === 'local';
    return {
      kind: d.kind,
      base_url: (address ? s.address.trim() : d.base) || undefined,
      api_key: s.key.trim() || undefined,
      model: s.main || (d.custom ? s.typedModel.trim() : '') || (r.source === 'local' ? bestModel(detected) : d.model) || undefined,
      allow_private: s.allowPrivate,
    };
  }

  async function check(v: string, o: { byPerson?: boolean; label?: string } = {}) {
    clearTimeout(timer);
    const id = ++seq;
    const r = row(v);
    const s = states[v];
    s.status = 'checking';
    s.checking = o.label ?? checkingLabel(r);
    s.failure = null;
    s.byPerson = !!o.byPerson;
    const req = request(r, s);
    let probe: ProbeV2 | null = null;
    let raw = '';
    let status: number | undefined;
    try {
      probe = await api.post<ProbeV2>('/api/providers/test', req);
    } catch (e) {
      raw = errorText(e);
      status = e instanceof ApiError ? e.status : undefined;
    }
    if (id !== seq) return;
    if (probe?.models?.length) s.models = probe.models;
    if (probe?.ok) {
      const pick = pickModels(r.def, s.models);
      s.main = req.model || pick.main;
      s.quick = pick.quick === pick.main ? s.main : pick.quick;
      s.note = modelNote(probe.context, probe.tool_calling);
      s.status = 'connected';
      s.needsModel = false;
      if (!setup) void save();
      return;
    }
    const detail = probe?.detail || raw;
    const code = failureCode({ code: probe?.code, detail, status, aws: r.def.kind === 'bedrock' });
    // An address that answers with an empty model list: ask for the model by name (§5.6.2).
    if (r.def.custom && code === 'no_models' && !s.typedModel.trim()) {
      s.needsModel = true;
      s.status = 'idle';
      return;
    }
    s.failure = failure(code, {
      name: r.def.name,
      local: !!(r.def.local || r.def.custom || r.source === 'local'),
      model: req.model ? friendlyModel(req.model) : undefined,
      region: regionName(s.region),
      regionCode: s.region,
      source: probe?.source ?? r.detection?.source,
      hostPort: req.base_url ? hostPort(req.base_url) : undefined,
      console: r.def.console,
      ollama: r.def.kind === 'ollama',
    });
    s.details = [detail, ...(s.failure.more ?? [])].filter(Boolean).join('\n');
    s.rejected = code === 'key_rejected';
    // The select opens on the model that was refused, so choosing another one re-checks.
    s.changing = code === 'no_model_access' && s.models.length > 0;
    if (s.changing) s.main = req.model ?? '';
    s.status = 'failed';
  }

  function allow(v: string) {
    states[v].allowPrivate = true;
    void check(v, { byPerson: true });
  }

  function chooseModel(v: string, model: string) {
    states[v].main = model;
    void check(v, { label: 'Checking…' });
  }

  function focusChoices() {
    const radios = [...(root?.querySelectorAll<HTMLInputElement>('input[type=radio]') ?? [])];
    (radios.find((r) => r.checked) ?? radios[0])?.focus();
  }

  // One commit per intent (D13): the key, the provider and both model slots
  // are written only here. Until S6 lands these are the existing calls in sequence.
  async function save() {
    if (!selected || !ready || saving) return;
    const r = row(selected);
    const s = states[selected];
    const d = r.def;
    saving = true;
    saveError = '';
    try {
      let key = false;
      if (r.source === 'env' && d.key) {
        await api.post('/api/vault/import', { env_var: r.detection?.env_var, name: d.key });
        key = true;
      } else if (d.key && s.key.trim()) {
        await api.put(`/api/vault/global/${encodeURIComponent(d.key)}`, { value: s.key.trim() });
        key = true;
      }
      const address = d.local || d.custom || r.source === 'local' ? s.address.trim() : undefined;
      const set: Record<string, unknown> = {
        [`providers.${d.id}`]: providerEntry(d, { base: address, key, allowPrivate: s.allowPrivate, region: d.kind === 'bedrock' ? s.region : undefined }),
      };
      if (setup) {
        set['models.main'] = [`${d.id}/${s.main}`];
        set['models.fast'] = [`${d.id}/${s.quick || s.main}`];
      }
      await api.patch('/api/config', { set });
      onconnected?.({ id: d.id, name: d.name, main: s.main, quick: s.quick || s.main });
    } catch (e) {
      saveError = "Bothy couldn't save this connection. Try again. If it keeps happening, check that the server's data folder isn't full or read-only.";
      saveDetails = errorText(e);
    } finally {
      saving = false;
    }
  }
</script>

{#snippet regionSelect(v: string)}
  <Select
    class="[&_select]:text-ellipsis"
    label="Region"
    options={regions.map(([value, label]) => ({ value, label, description: value }))}
    value={states[v].region}
    onchange={(e) => {
      const s = states[v];
      s.region = e.currentTarget.value;
      s.main = '';
      s.changing = false;
      if (row(v).source === 'aws' || s.status !== 'idle') void check(v, { label: 'Checking which models you can use…' });
    }}
  />
{/snippet}

{#snippet link(href: string, label: string, cls: string)}
  <a class="link {cls}" {href} target="_blank" rel="noopener noreferrer">{label}<span aria-hidden="true">{'\u00a0↗'}</span><span class="sr-only">{' (opens in a new tab)'}</span></a>
{/snippet}

{#snippet status(v: string, first: boolean)}
  {@const s = states[v]}
  {@const r = row(v)}
  {@const gap = first && !s.changing ? '' : 'mt-3'}
  {#if s.changing && s.models.length}
    <!-- Native selects clip a long value at 360 px; the ellipsis shows it was cut. -->
    <Select class="{first ? '' : 'mt-3'} [&_select]:text-ellipsis" label="Model" options={modelOptions(s.models, pickModels(r.def, s.models).main)} value={s.main} onchange={(e) => chooseModel(v, e.currentTarget.value)} />
  {/if}
  {#if s.status === 'checking'}
    <StatusLine class={gap} tone="progress" text={s.checking} />
  {:else if s.status === 'connected'}
    {#if !s.changing}
      <StatusLine
        class={gap}
        tone="success"
        text={setup ? `Connected. Bothy will use ${friendlyModel(s.main)}.` : 'Connected.'}
        action={setup && s.models.length > 1 ? { label: 'Change', onclick: () => (s.changing = true) } : undefined}
      />
    {/if}
    {#if s.note}<p class="mt-2 text-small text-muted {s.changing ? '' : 'pl-6'}">{s.note}</p>{/if}
  {:else if s.status === 'failed' && s.failure}
    {@const f = s.failure}
    <div class="text-body {gap}">
      <div class="flex items-start gap-2">
        <span class="flex h-5 w-4 shrink-0 items-center justify-center"><Icon name="alert" class="text-danger-text" /></span>
        <p id="{uid}-{v}-status" class="min-w-0 flex-1 text-danger-text" role={s.byPerson ? 'alert' : undefined}>
          {#if f.command}
            {@const [before, after] = f.text.split(f.command)}
            {before}<code class="rounded-sm bg-subtle px-1 text-code">{f.command}</code>{after}
          {:else}
            {f.text}
          {/if}
          {#if f.action?.kind === 'retry'}
            <LinkButton class="ml-1" onclick={() => check(v, { byPerson: true })}>{f.action.label}</LinkButton>
          {:else if f.action?.kind === 'link'}
            {@render link(f.action.href, f.action.label, 'ml-1')}
          {/if}
        </p>
      </div>
      {#if f.action?.kind === 'allow'}
        <Button class="mt-3 ml-6" size="sm" onclick={() => allow(v)}>{f.action.label}</Button>
      {/if}
      {#if s.details}
        <Disclosure class="mt-1 pl-6"><CodeBlock code={s.details} wrap /></Disclosure>
      {/if}
    </div>
  {/if}
{/snippet}

{#snippet keyField(v: string, label: string, help: boolean)}
  {@const r = row(v)}
  {@const s = states[v]}
  <div>
    <div class="flex items-baseline justify-between gap-3">
      <label for="{uid}-{v}-key" class="field-label">{label}</label>
      <span class="text-small"><LinkButton onclick={() => (reveal = !reveal)} aria-controls="{uid}-{v}-key" aria-pressed={reveal}>{reveal ? 'Hide' : 'Show'}</LinkButton></span>
    </div>
    <input
      id="{uid}-{v}-key"
      class="input mt-2"
      type={reveal ? 'text' : 'password'}
      autocomplete="off"
      autocapitalize="off"
      spellcheck="false"
      bind:value={states[v].key}
      aria-invalid={s.status === 'failed' && s.rejected ? 'true' : undefined}
      aria-describedby={s.status === 'failed' ? `${uid}-${v}-status` : help && s.status === 'idle' ? `${uid}-${v}-help` : undefined}
      oninput={(e) => edited(v, (e as unknown as InputEvent).inputType === 'insertFromPaste')}
      onkeydown={(e) => enter(e, v)}
    />
    {#if help && r.def.console && s.status === 'idle'}
      <p id="{uid}-{v}-help" class="field-help">
        Stored encrypted. Only sent to {r.def.name}.
        {@render link(r.def.console, `Get a key at ${consoleLabel(r.def.console)}`, '')}
      </p>
    {/if}
  </div>
{/snippet}

{#snippet expand(v: string)}
  {@const r = row(v)}
  {@const s = states[v]}
  {#if s}
    {#if r.source === 'aws'}
      {@render regionSelect(v)}
      {@render status(v, false)}
    {:else if r.def.kind === 'bedrock'}
      <p class="text-body text-text">Bothy uses the AWS access of the server it runs on, so there's no key to paste. No keys are stored in Bothy.</p>
      <div class="mt-4">{@render regionSelect(v)}</div>
      <div class="mt-4 flex flex-wrap items-center gap-x-4 gap-y-2">
        <Button size="sm" onclick={() => check(v, { byPerson: true })}>Check access</Button>
        {@render link(AWS_HELP, 'How to give Bothy AWS access', 'inline-flex min-h-6 items-center text-body pointer-coarse:min-h-11')}
      </div>
      {@render status(v, false)}
    {:else if r.source === 'list' && (r.def.local || r.def.custom)}
      <TextField
        label="Server address"
        type="url"
        bind:value={states[v].address}
        placeholder={r.def.custom ? 'https://models.example.com/v1' : undefined}
        autocomplete="off"
        autocapitalize="off"
        spellcheck="false"
        aria-describedby={s.status === 'failed' ? `${uid}-${v}-status` : undefined}
        oninput={() => edited(v)}
        onkeydown={(e) => enter(e, v)}
      />
      {#if r.def.custom}
        <div class="mt-5">{@render keyField(v, 'API key (if needed)', false)}</div>
        {#if s.needsModel}
          <TextField
            class="mt-5"
            label="Model name"
            help="The server didn't list its models, so name the one to use."
            bind:value={states[v].typedModel}
            autocomplete="off"
            autocapitalize="off"
            spellcheck="false"
            oninput={() => {
              clearTimeout(timer);
              if (states[v].typedModel.trim()) timer = setTimeout(() => check(v), IDLE_MS);
            }}
            onkeydown={(e) => e.key === 'Enter' && (e.preventDefault(), check(v, { byPerson: true }))}
          />
        {/if}
      {/if}
      {@render status(v, false)}
    {:else if r.source === 'list'}
      {@render keyField(v, 'API key', true)}
      {@render status(v, false)}
    {:else}
      {@render status(v, true)}
    {/if}
  {/if}
{/snippet}

<div bind:this={root}>
  {#if found.length}
    <h2 class="text-small font-medium text-muted">Found on this server</h2>
    <OptionList class="mt-2" name="{uid}-found" label="Found on this server" options={found} framed={setup} bind:value={selected} onchange={select} expanded={expand} />
  {:else if looking}
    <p class="text-body text-muted" role="status">Looking for models on this server…</p>
  {/if}
  <h2 class="text-small font-medium text-muted {found.length || looking ? 'mt-8' : ''}">{found.length || looking ? 'Or choose a provider' : 'Choose a provider'}</h2>
  <!-- One card holds both lists and the More providers row, so the lists are unframed and take a framed list's row padding from here. -->
  <div class="mt-2 {setup ? 'card p-0' : ''}">
    <OptionList name="{uid}-list" label="Providers" options={base} framed={false} class={pad} bind:value={selected} onchange={select} expanded={expand} />
    <div class="border-t border-border">
      <button
        type="button"
        class="flex min-h-12 w-full cursor-pointer items-center gap-2 text-left text-body font-medium text-text {setup ? 'px-4' : ''}"
        aria-expanded={showMore}
        aria-controls="{uid}-more"
        onclick={() => (more = !showMore)}
      >
        <Icon name="chevron-right" size={16} class="text-muted transition-transform duration-160 {showMore ? 'rotate-90' : ''}" />
        More providers
      </button>
    </div>
    <div id="{uid}-more">
      {#if showMore}
        <div class="border-t border-border">
          <OptionList name="{uid}-more" label="More providers" options={extra} framed={false} class={pad} bind:value={selected} onchange={select} expanded={expand} />
        </div>
      {/if}
    </div>
  </div>

  {#if setup}
    <div class="mt-8 flex flex-wrap items-center gap-x-4 gap-y-2 max-sm:flex-col max-sm:items-stretch">
      <Button
        variant="primary"
        class="h-10 max-sm:w-full pointer-coarse:h-11"
        reason={ready ? undefined : 'Connect a model to continue.'}
        onblocked={focusChoices}
        loading={saving}
        loadingLabel="Saving…"
        onclick={save}>Continue</Button
      >
    </div>
  {/if}
  {#if saveError}
    <div class="mt-3">
      <p class="field-error" role="alert"><Icon name="alert" class="mt-px" /><span class="min-w-0">{saveError}</span></p>
      {#if saveDetails}<Disclosure class="mt-1"><CodeBlock code={saveDetails} wrap /></Disclosure>{/if}
    </div>
  {/if}
</div>
