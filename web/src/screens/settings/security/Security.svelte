<script lang="ts">
  import { tick, untrack } from 'svelte';
  import Icon from '../../../components/Icon.svelte';
  import PauseDialog from '../../../components/shell/PauseDialog.svelte';
  import { Button, CodeBlock, Disclosure, LinkButton, OptionList, Page, Select, SettingsRow, SettingsSection, Switch, toast, type Option } from '../../../components/ui';
  import { api } from '../../../lib/api/client';
  import type { DoctorReport, Finding, Grant, SandboxInfo, SecretMeta, SignInAdmin, VaultStatus } from '../../../lib/api/types.gen';
  import { config, saveErrorText } from '../../../lib/config.svelte';
  import { plural } from '../../../lib/format';
  import { pausedSentence } from '../../../components/shell/banners';
  import { router } from '../../../lib/router.svelte';
  import { session } from '../../../lib/stores/session.svelte';
  import { describeFinding, needsAttention } from './findings';
  import LinkRow from './LinkRow.svelte';
  import { settingsBack } from './page.svelte';
  import PageState from './PageState.svelte';
  import { listProblem, listSaveError, listText, parseList, rowValue } from './signin';
  import { chatChanges, chatChoice, scheduledChanges, scheduledChoice, type ApprovalConfig } from './timeouts';
  import UnlockDialog from './UnlockDialog.svelte';

  const uid = $props.id();
  const presets: Option[] = [
    { value: 'careful', title: 'Careful', description: 'Asks before sending anything out, opening a pull request or changing a file.' },
    { value: 'balanced', title: 'Balanced', description: "Asks the first time it contacts a new person or website, and before anything that can't be undone." },
    { value: 'hands_off', title: 'Hands-off', description: 'Asks only before deleting, deploying or spending money.' },
  ];
  // The compose deployment from the README, for servers that don't send their own lines.
  const COMPOSE = 'sh install.sh --compose ~/bothy\ncd ~/bothy && docker compose up -d';
  const DEPLOY_GUIDE = 'https://github.com/rielicat/bothy/blob/main/docs/deploy/README.md';

  let audit = $state<Finding[] | null>(null);
  let auditFailed = $state(false);
  let sandbox = $state<SandboxInfo | null>(null);
  let grants = $state<Grant[] | null>(null);
  let keys = $state<SecretMeta[] | null>(null);
  let vault = $state<VaultStatus | null>(null);

  type RowState = { status: 'saving' | 'saved' | null; error?: string };
  let rows = $state<Record<string, RowState>>({});
  const savedTimers: Record<string, ReturnType<typeof setTimeout>> = {};
  let attentionList: HTMLElement | undefined = $state();
  let openFinding = $state<number | null>(null);
  let fixing = $state<number | null>(null);
  let fixError = $state<{ i: number; text: string } | null>(null);
  let howOpen = $state(false);
  let howButton: HTMLButtonElement | undefined = $state();
  let pausing = $state(false);
  let unlocking = $state(false);
  let resuming = $state(false);
  let resumeError = $state('');

  // The Running code section says it, with how to turn it on; the locked-keys banner says the other, with Unlock.
  const sealedBanner = $derived(session.banners.some((b) => b.code === 'vault_sealed') && !session.dismissed.includes('vault_sealed'));
  const keyFile = $derived(!!vault?.kek_source && vault.kek_source !== 'passphrase');
  const attention = $derived(
    needsAttention(audit).filter((f) => {
      const a = describeFinding(f, keyFile).action;
      return !(a === 'running-code' && codeOn === false) && !(f.id === 'vault.sealed' && sealedBanner);
    }),
  );
  const approval = $derived<ApprovalConfig>({
    interactiveTimeout: config.value('policy.approval.interactive_timeout'),
    interactiveOnTimeout: config.value('policy.approval.interactive_on_timeout'),
    scheduledTimeout: config.value('policy.approval.scheduled_timeout'),
    scheduledOnTimeout: config.value('policy.approval.scheduled_on_timeout'),
    parkTtl: config.value('policy.approval.park_ttl'),
  });
  const chat = $derived(chatChoice(approval));
  const scheduled = $derived(scheduledChoice(approval));
  const sites = $derived((config.value<string[]>('egress.allow', []) ?? []).length);
  const codeOn = $derived(sandbox?.status.available);
  const fixed = (path: string) => {
    const e = config.entry(path);
    return !!e && (e.locked || e.source.layer === 'env');
  };
  const presetFixed = $derived(fixed('policy.preset'));
  const chatFixed = $derived(fixed('policy.approval.interactive_on_timeout') || fixed('policy.approval.interactive_timeout'));
  const scheduledFixed = $derived(fixed('policy.approval.scheduled_on_timeout') || fixed('policy.approval.scheduled_timeout'));
  const FIXED_HELP = "This is set on the server and can't be changed here.";

  // The banner's first sentence; with nothing known it also says what pausing does.
  const pausedText = $derived.by(() => {
    const s = pausedSentence(session.pause);
    return s === 'Bothy is paused.' ? `${s} Nothing new starts until someone resumes it.` : s;
  });

  async function runAudit(fix = false): Promise<boolean> {
    try {
      const r = await api.post<DoctorReport>('/api/security-audit', { fix });
      audit = r.findings ?? [];
      auditFailed = false;
      return true;
    } catch {
      if (!fix) auditFailed = true;
      return false;
    }
  }

  function loadSandbox() {
    api.get<SandboxInfo>('/api/sandbox').then((s) => (sandbox = s), () => {});
  }

  // Sign-in (§11.6): one list of who may sign in with Google or GitHub.
  let signIn = $state<SignInAdmin | null>(null);
  let allowText = $state('');
  let allowBase = '';
  let allowRow = $state<RowState>({ status: null });
  let allowNewRow = $state<RowState>({ status: null });

  function setSignIn(a: SignInAdmin, replace = false) {
    signIn = a;
    const text = listText(a.allow);
    // A list someone is still typing stays as it is.
    if (replace || allowText === allowBase) allowText = text;
    allowBase = text;
  }

  function loadSignIn() {
    api.get<SignInAdmin>('/api/sign-in').then((a) => setSignIn(a), () => {});
  }

  async function saveAllow() {
    const list = parseList(allowText);
    if (listText(list) === allowBase) {
      allowText = allowBase;
      allowRow = { status: null };
      return;
    }
    const problem = listProblem(list);
    if (problem) {
      allowRow = { status: null, error: problem };
      return;
    }
    allowRow = { status: 'saving' };
    try {
      setSignIn(await api.patch<SignInAdmin>('/api/sign-in', { allow: list }), true);
      allowRow = { status: 'saved' };
      void config.load(true);
    } catch (e) {
      allowRow = { status: null, error: listSaveError(e) };
    }
  }

  async function saveAllowNew(v: boolean) {
    allowNewRow = { status: 'saving' };
    try {
      setSignIn(await api.patch<SignInAdmin>('/api/sign-in', { allow_new: v }));
      allowNewRow = { status: 'saved' };
    } catch {
      allowNewRow = { status: null, error: "Couldn't save. Try again." };
    }
  }

  function load() {
    void config.load(true);
    void runAudit();
    loadSandbox();
    loadSignIn();
    api.get<Grant[]>('/api/grants').then((g) => (grants = g ?? []), () => {});
    api.get<SecretMeta[]>('/api/vault?scope=global').then((k) => (keys = k ?? []), () => {});
    api.get<VaultStatus>('/api/vault/status').then((v) => (vault = v), () => {});
  }

  $effect(() => {
    if (session.isAdmin) untrack(load);
  });

  // The server announces a change of the isolated workspace on the event stream.
  let lastSandbox: boolean | undefined;
  $effect(() => {
    const now = session.sandbox;
    if (lastSandbox !== undefined && now !== lastSandbox) loadSandbox();
    lastSandbox = now;
  });

  async function save(key: string, changes: Record<string, unknown> | null) {
    if (!changes) return;
    rows[key] = { status: 'saving' };
    clearTimeout(savedTimers[key]);
    const r = await config.save(changes);
    rows[key] = r.ok ? { status: 'saved' } : { status: null, error: saveErrorText(r) };
    // "Saved" is shown for 2 s (§11.1). SettingsRow times its own; the preset's label lives on this page.
    if (r.ok) savedTimers[key] = setTimeout(() => rows[key]?.status === 'saved' && (rows[key] = { status: null }), 2000);
  }

  async function fix(i: number) {
    fixing = i;
    fixError = null;
    const ok = await runAudit(true);
    fixing = null;
    if (!ok) {
      fixError = { i, text: "Couldn't fix this. Try again." };
      return;
    }
    toast('Fixed.');
    await runAudit();
    // The fixed row is gone: focus the next finding's action, else the page title, never the body.
    await tick();
    const actions = attentionList?.querySelectorAll<HTMLElement>('[data-finding-action]') ?? [];
    (actions[Math.min(i, actions.length - 1)] ?? document.querySelector<HTMLElement>('h1'))?.focus();
  }

  function act(f: Finding, i: number) {
    const a = describeFinding(f, keyFile).action;
    if (a === 'fix') void fix(i);
    else if (a === 'unlock') unlocking = true;
    else if (a === 'running-code') {
      howOpen = true;
      howButton?.scrollIntoView({ block: 'center' });
      howButton?.focus();
    } else openFinding = openFinding === i ? null : i;
  }

  const actionLabel = { fix: 'Fix', how: 'How to fix', 'running-code': 'How to turn it on', unlock: 'Unlock' };

  async function resume() {
    resuming = true;
    resumeError = '';
    try {
      await api.post('/api/admin/pause', { paused: false });
      session.paused = false;
      toast('Bothy resumed.');
    } catch {
      resumeError = "Couldn't resume Bothy. Try again.";
    } finally {
      resuming = false;
    }
  }

  // The paused banner names who paused and when; sign-in data says it too, for tabs without the stream.
  function paused() {
    void session.refresh().catch(() => {});
  }
</script>

{#snippet howToSwitch()}
  <a class="link -my-0.5 inline-flex min-h-6 items-center pointer-coarse:-my-3 pointer-coarse:min-h-11" href={DEPLOY_GUIDE} target="_blank" rel="noopener noreferrer"
    >How to switch<span aria-hidden="true">&nbsp;↗</span><span class="sr-only"> (opens in a new tab)</span></a
  >
{/snippet}

<Page title="Security" back={settingsBack()}>
  {#if !session.isAdmin}
    <PageState kind="forbidden" />
  {:else if !config.view && config.error}
    <PageState kind="failed" onretry={load} />
  {:else if !config.view}
    <PageState kind="loading" />
  {:else}
    {#if attention.length || auditFailed}
      <SettingsSection title="Needs attention">
        {#if auditFailed}
          <p class="py-3 text-body text-muted">Couldn't check for problems. <LinkButton onclick={() => runAudit()}>Try again</LinkButton></p>
        {/if}
        <div bind:this={attentionList}>
          {#each attention as f, i (i)}
            {@const c = describeFinding(f, keyFile)}
            <div class="border-b border-border py-3 last:border-b-0">
              <div class="grid min-h-8 items-center gap-x-6 gap-y-2 sm:grid-cols-[minmax(0,1fr)_auto]">
                <p class="text-body text-text">{c.text}</p>
                <div class="justify-self-start sm:justify-self-end">
                  <Button
                    size="sm"
                    data-finding-action
                    loading={fixing === i}
                    loadingLabel="Fixing…"
                    aria-expanded={c.action === 'how' ? openFinding === i : undefined}
                    aria-controls={c.action === 'how' ? `${uid}-finding-${i}` : undefined}
                    onclick={() => act(f, i)}>{actionLabel[c.action]}</Button
                  >
                </div>
              </div>
              {#if fixError?.i === i}<p class="field-error" role="alert"><Icon name="alert" class="mt-px" />{fixError.text}</p>{/if}
              {#if c.action === 'how' && openFinding === i}
                <div id="{uid}-finding-{i}" class="mt-2 max-w-prose" data-details>
                  {#if c.steps}<p class="text-body text-text">{c.steps}</p>{/if}
                  {#if f.detail || f.fix}
                    <Disclosure class="mt-2">
                      <div class="space-y-2">
                        {#if f.detail}<p class="text-small text-muted">{f.detail}</p>{/if}
                        {#if f.fix}<CodeBlock code={f.fix} wrap />{/if}
                      </div>
                    </Disclosure>
                  {/if}
                </div>
              {/if}
            </div>
          {/each}
        </div>
      </SettingsSection>
    {/if}

    <SettingsSection title="Approvals">
      <div class="py-3">
        <div class="flex flex-wrap items-baseline gap-x-2">
          <p id="{uid}-ask" class="text-body font-medium text-text">When should Bothy ask?</p>
          <span class="text-small text-muted" aria-live="polite">{rows.preset?.status === 'saving' ? 'Saving…' : rows.preset?.status === 'saved' ? 'Saved' : ''}</span>
        </div>
        {#if presetFixed}<p class="mt-0.5 text-small text-muted">{FIXED_HELP}</p>{/if}
        <OptionList
          class="mt-3"
          name="{uid}-preset"
          label="When should Bothy ask?"
          options={presets.map((p) => ({ ...p, disabled: presetFixed }))}
          value={config.value<string>('policy.preset', 'careful')}
          onchange={(v) => save('preset', { 'policy.preset': v })}
        />
        {#if rows.preset?.error}<p class="field-error" role="alert"><Icon name="alert" class="mt-px" />{rows.preset.error}</p>{/if}
      </div>
      <SettingsRow label="If nobody answers a chat request" help={chatFixed ? FIXED_HELP : undefined} status={rows.chat?.status} error={rows.chat?.error}>
        {#snippet children(c)}
          <Select
            id={c.id}
            aria-describedby={c.describedBy}
            disabled={chatFixed}
            options={chat.options}
            value={chat.value}
            onchange={(e) => save('chat', chatChanges(e.currentTarget.value))}
          />
        {/snippet}
      </SettingsRow>
      <SettingsRow label="If nobody answers a scheduled request" help={scheduledFixed ? FIXED_HELP : undefined} status={rows.scheduled?.status} error={rows.scheduled?.error}>
        {#snippet children(c)}
          <Select
            id={c.id}
            aria-describedby={c.describedBy}
            disabled={scheduledFixed}
            options={scheduled.options}
            value={scheduled.value}
            onchange={(e) => save('scheduled', scheduledChanges(e.currentTarget.value))}
          />
        {/snippet}
      </SettingsRow>
      <LinkRow label="Allowed without asking" value={grants === null ? '' : grants.length ? plural(grants.length, 'permission') : 'None'} href="/settings/security/permissions" />
    </SettingsSection>

    {#if signIn}
      {@const listFixed = fixed('auth.sign_in.allow')}
      {@const newFixed = fixed('auth.sign_in.allow_new')}
      {@const orgsSet = signIn.providers.some((p) => p.provider === 'github' && p.orgs.length > 0)}
      <SettingsSection title="Sign-in">
        <SettingsRow label="Who can sign in" help={listFixed ? FIXED_HELP : 'One email address or domain per line, like acme.com or pat@gmail.com.'}
          status={allowRow.status}
          error={allowRow.error}
        >
          {#snippet children(c)}
            <textarea
              id={c.id}
              class="input resize-y"
              rows="3"
              bind:value={allowText}
              placeholder="acme.com"
              autocapitalize="none"
              spellcheck="false"
              disabled={listFixed}
              aria-describedby={c.describedBy}
              aria-invalid={c.invalid || undefined}
              onblur={saveAllow}
            ></textarea>
          {/snippet}
        </SettingsRow>
        <SettingsRow
          label="Create accounts on first sign-in"
          inline
          help={newFixed
            ? FIXED_HELP
            : signIn.team_mode
              ? `${orgsSet ? 'Members of the GitHub organizations you add also get an account. ' : ''}People who already have an account should connect Google or GitHub in Account first.`
              : "This Bothy is set up for one person, so it can't create accounts. Whoever runs the server can switch it to team use."}
          note={!newFixed && !signIn.team_mode ? howToSwitch : undefined}
          status={allowNewRow.status}
          error={allowNewRow.error}
        >
          {#snippet children(c)}
            <Switch
              id={c.id}
              label="Create accounts on first sign-in"
              hideLabel
              aria-describedby={c.describedBy}
              checked={signIn?.allow_new ?? false}
              disabled={newFixed || !signIn?.team_mode}
              onchange={saveAllowNew}
            />
          {/snippet}
        </SettingsRow>
        {#each signIn.providers as st (st.provider)}
          {@const v = rowValue(st)}
          <LinkRow label={st.name} value={v.text} tone={v.tone} href="/settings/security/{st.provider}-sign-in" />
        {/each}
      </SettingsSection>
    {/if}

    <SettingsSection title="Running code">
      {#if sandbox}
        <div class="border-b border-border py-3">
          <div class="flex min-h-8 flex-wrap items-center justify-between gap-x-6 gap-y-1">
            <p class="text-body text-text">{codeOn ? 'On. Code runs in an isolated workspace.' : "Off on this server. Bothy can't run commands or scripts."}</p>
            {#if !codeOn}
              <button
                bind:this={howButton}
                type="button"
                class="-my-0.5 inline-flex min-h-6 cursor-pointer items-center gap-1 text-body text-text hover:text-muted pointer-coarse:min-h-11"
                aria-expanded={howOpen}
                aria-controls="{uid}-how"
                onclick={() => (howOpen = !howOpen)}
                ><Icon name="chevron-right" size={12} class="transition-transform duration-160 {howOpen ? 'rotate-90' : ''}" />How to turn it on</button
              >
            {/if}
          </div>
          <!-- One level: the sentence and the commands. Backend, runtime and the reason are in Advanced → Running code. -->
          {#if !codeOn && howOpen}
            <div id="{uid}-how" class="mt-3 max-w-prose space-y-3">
              <p class="text-body text-text">Start Bothy with the compose file from the deployment guide, which includes the isolated workspace service, then restart Bothy.</p>
              <CodeBlock code={sandbox.compose_hint || sandbox.status.fix || COMPOSE} label="Commands" />
            </div>
          {/if}
        </div>
      {/if}
      <!-- Package downloads only matter to code that runs, so they wait until it can. -->
      {#if codeOn !== false}
        {#each [['egress.presets.python_packages', 'Let code download Python packages'], ['egress.presets.npm_packages', 'Let code download JavaScript packages']] as [path, label] (path)}
          <SettingsRow {label} inline help={fixed(path) ? FIXED_HELP : undefined} status={rows[path]?.status} error={rows[path]?.error}>
            {#snippet children(c)}
              <Switch id={c.id} {label} hideLabel aria-describedby={c.describedBy} checked={config.flag(path)} disabled={fixed(path)} onchange={(v) => save(path, { [path]: v })} />
            {/snippet}
          </SettingsRow>
        {/each}
      {/if}
      <LinkRow label="Websites Bothy can reach" value={plural(sites, 'site')} href="/settings/security/websites" />
    </SettingsSection>

    <!-- The heading and its one value share a line, so the whole line is the link (§11.6 wireframe). -->
    <section class="mt-10" aria-labelledby="{uid}-keys">
      <a
        href="/settings/security/keys"
        onclick={router.link}
        class="-mx-2 -my-1 grid grid-cols-[minmax(0,1fr)_auto] items-center gap-x-6 rounded-md px-2 py-1 hover:bg-subtle focus-visible:outline-offset-[-2px] pointer-coarse:min-h-11 sm:grid-cols-[minmax(0,1fr)_minmax(0,20rem)]"
      >
        <h2 id="{uid}-keys" class="section-title">Saved keys</h2>
        <span class="inline-flex items-center gap-1 text-body {vault?.sealed ? 'text-danger-text' : 'text-muted'}"
          >{vault?.sealed ? 'Locked' : keys === null ? '' : keys.length ? plural(keys.length, 'saved key') : 'None'}<Icon name="chevron-right" /></span
        >
      </a>
    </section>

    <SettingsSection title="Pause Bothy">
      {#if session.paused}
        <p class="text-body text-text">{pausedText}</p>
        <Button variant="primary" class="mt-3" loading={resuming} loadingLabel="Resuming…" onclick={resume}>Resume</Button>
        {#if resumeError}<p class="field-error" role="alert"><Icon name="alert" class="mt-px" />{resumeError}</p>{/if}
      {:else}
        <p class="text-body text-muted">Stops everything that's running and keeps anything new from starting until someone resumes it.</p>
        <Button class="mt-3 text-danger-text" onclick={() => (pausing = true)}>Pause Bothy…</Button>
      {/if}
    </SettingsSection>
  {/if}
</Page>

<PauseDialog bind:open={pausing} onpaused={paused} />
<UnlockDialog
  bind:open={unlocking}
  first={!!vault?.new_passphrase}
  onunlocked={() => {
    vault = vault ? { ...vault, sealed: false } : vault;
    void runAudit();
  }}
/>
