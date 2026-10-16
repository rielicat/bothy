<script lang="ts">
  // /dev/ui, development builds only (App.svelte imports it behind import.meta.env.DEV):
  // every primitive of docs/design/ui.md §15.3 in each of its states, for review.
  import { onMount } from 'svelte';
  import { lineDiff } from '../../lib/diff';
  import { theme, type ThemePref } from '../../lib/stores/ui.svelte';
  import Icon from '../Icon.svelte';
  import {
    Avatar,
    Banner,
    Button,
    Card,
    Checkbox,
    CodeBlock,
    Combobox,
    CommandBlock,
    Count,
    Dialog,
    DiffView,
    Disclosure,
    Dot,
    Drawer,
    EmptyState,
    FileToken,
    IconButton,
    Kbd,
    KeyField,
    LinkButton,
    ListRow,
    Listbox,
    LiveRegion,
    Menu,
    Notice,
    OptionList,
    Page,
    ProgressBar,
    Radio,
    Segmented,
    Select,
    SettingsRow,
    SettingsSection,
    Spinner,
    StatusLine,
    Switch,
    Tabs,
    TextArea,
    TextField,
    ToastHost,
    ToggleButton,
    Tooltip,
    toast,
    type CheckResult,
    type MenuItem,
  } from './index';

  const sections = [
    ['buttons', 'Buttons'],
    ['fields', 'Fields'],
    ['choices', 'Choices'],
    ['navigation', 'Menus, lists and tabs'],
    ['overlays', 'Dialogs, drawers and toasts'],
    ['status', 'Status'],
    ['content', 'Rows and settings'],
    ['code', 'Code and files'],
  ] as const;

  // Buttons
  let saving = $state(false);
  function save() {
    saving = true;
    setTimeout(() => (saving = false), 1500);
  }
  let refine = $state(false);
  let refineOn = $state(true);

  // Fields
  let name = $state('');
  let limit = $state('5.00');
  let password = $state('');
  let showPassword = $state(false);
  let persona = $state('Warm and brief. Uses plain words and short answers, and asks before doing anything that leaves the team.');
  let rule = $state('0 9 * * 1-5');
  let region = $state('eu-west-1');
  let zone = $state('Europe/London');
  let people = $state<string[]>(['alex', 'priya']);

  const zones = [
    { value: 'Europe/London', label: 'London' },
    { value: 'America/New_York', label: 'New York' },
    { value: 'America/Sao_Paulo', label: 'São Paulo' },
    { value: 'America/Argentina/Buenos_Aires', label: 'Buenos Aires' },
    { value: 'Asia/Tokyo', label: 'Tokyo' },
    { value: 'Australia/Sydney', label: 'Sydney' },
  ];
  const team = [
    { value: 'alex', label: 'Alex Kim', description: 'Owner' },
    { value: 'sam', label: 'Sam Patel', description: 'Admin' },
    { value: 'jordan', label: 'Jordan Lee' },
    { value: 'priya', label: 'Priya Shah' },
  ];

  // KeyField: each instance is put in its state on mount.
  const never = (_v: string, signal: AbortSignal) => new Promise<CheckResult>((_resolve, reject) => signal.addEventListener('abort', () => reject(new Error('aborted'))));
  const connects = async (): Promise<CheckResult> => ({ ok: true, message: 'Connected. Bothy will use Claude Sonnet 4.5.' });
  const rejects = async (): Promise<CheckResult> => ({
    ok: false,
    message: "Anthropic didn't accept this key. Check that you copied all of it.",
    details: '401 authentication_error: invalid x-api-key',
    action: { label: 'Get a new key ↗', onclick: () => {} },
  });
  let keyIdle = $state('');
  let keyChecking = $state('sk-ant-api03-checking-0000000000000000');
  let keyConnected = $state('sk-ant-api03-connected-000000000000000');
  let keyFailed = $state('sk-ant-api03-wrong-00000000000000000000');
  let kfChecking: { checkNow: () => void } | undefined = $state();
  let kfConnected: { checkNow: () => void } | undefined = $state();
  let kfFailed: { checkNow: () => void } | undefined = $state();
  onMount(() => {
    kfChecking?.checkNow();
    kfConnected?.checkNow();
    kfFailed?.checkNow();
  });

  // Choices
  let notify = $state(true);
  let weekly = $state(false);
  let approver = $state('admins');
  let switchOn = $state(true);
  let switchOff = $state(false);
  let period = $state('7');
  let preset = $state('careful');
  let customHours = $state('2');

  // Navigation
  let model = $state('sonnet');
  let detailsOpen = $state(false);
  let lastPick = $state('');
  const themeItems: MenuItem[] = $derived(
    (
      [
        ['system', 'Match the system'],
        ['light', 'Light'],
        ['dark', 'Dark'],
      ] as [ThemePref, string][]
    ).map(([v, label]) => ({ label, checked: theme.pref === v, group: 'Theme', onselect: () => theme.set(v) })),
  );
  const chatItems: MenuItem[] = [
    { label: 'Rename', onselect: () => (lastPick = 'Rename') },
    { label: 'Pin', onselect: () => (lastPick = 'Pin') },
    { label: 'Continue in a clean chat', onselect: () => (lastPick = 'Continue in a clean chat') },
    { label: 'Share', disabled: true, reason: 'Only the person who started the chat can share it.', onselect: () => {} },
    'separator',
    { label: 'Delete chat…', danger: true, onselect: () => (confirmOpen = true) },
  ];
  const models = [
    { value: 'sonnet', label: 'Claude Sonnet 4.5', description: 'Recommended', group: 'Anthropic' },
    { value: 'haiku', label: 'Claude Haiku 4.5', description: 'Faster, for quick tasks', group: 'Anthropic' },
    { value: 'qwen', label: 'Qwen3 14B', description: 'Free, on this server', group: 'Ollama' },
  ];

  // Overlays
  let confirmOpen = $state(false);
  let formOpen = $state(false);
  let reviewOpen = $state(false);
  let drawerOpen = $state(false);
  let navOpen = $state(false);
  let adding = $state(false);
  let newName = $state('');
  let newUsername = $state('');
  function addPerson() {
    adding = true;
    setTimeout(() => {
      adding = false;
      formOpen = false;
      newName = '';
      newUsername = '';
      toast('Person added.');
    }, 1200);
  }

  // Rows and settings
  let skillOn = $state(true);
  let rowStatus = $state<'saving' | 'saved' | null>(null);
  let rowLimit = $state('5.00');
  let rowZone = $state('Europe/London');
  let refineRow = $state(false);
  function saveRow() {
    rowStatus = 'saving';
    setTimeout(() => (rowStatus = 'saved'), 600);
  }
  let banners = $state({ restart: true });

  // Code
  const releaseDiff = `diff --git a/docs/release.md b/docs/release.md
--- a/docs/release.md
+++ b/docs/release.md
@@ -10,5 +10,6 @@ ## Releasing
 1. Freeze main and tell the team in #eng.
-2. Run the full test suite.
+2. Run the full test suite and the browser checks.
+3. Ask Sam or Priya to approve the deploy to production; the deploy waits for one of them before it starts, and the link to the release notes goes in the approval request.
 3. Tag the release.
 4. Publish the notes.
 5. Unfreeze main.
@@ -40,3 +41,3 @@ ## Rolling back
 Roll back from the release page.
-Tell #eng when it's done.
+Tell #eng and the support team when it's done.
 Write down what happened.`;
  const checklist = `# Launch checklist\n\n- Freeze main on Monday\n- Final browser checks\n- Approve the production deploy\n- Post the release notes`;
  const stagingNotes = `Staging runs the release branch.\nDeploys there need no approval.\nReset the database every Friday.`;
  const memoryDiff = lineDiff(
    'Alex prefers short weekly summaries on Monday mornings.\nThe team ships on Thursdays.',
    'Alex prefers short weekly summaries on Friday afternoons.\nThe team ships on Thursdays.\nRelease notes go to #announcements.',
  );
  const config = Array.from({ length: 12 }, (_, i) => `step_${i + 1}: echo "Checking part ${i + 1} of the release"`).join('\n');
</script>

<Page title="UI gallery" back={{ href: '/chat', label: 'Chat' }}>
  {#snippet actions()}
    <Menu items={themeItems} placement="bottom-end">
      {#snippet trigger(props)}<IconButton icon="more" label="Gallery options" {...props} />{/snippet}
    </Menu>
  {/snippet}

  <div id="gallery">
    <p class="text-reading text-muted">Every primitive from the v0.2 plan in each of its states. Only development builds have this page.</p>
    <div class="mt-4 flex flex-wrap items-center gap-x-4 gap-y-2">
      <Segmented label="Theme" options={[{ value: 'system', label: 'System' }, { value: 'light', label: 'Light' }, { value: 'dark', label: 'Dark' }]} value={theme.pref} onchange={(v) => theme.set(v as ThemePref)} />
    </div>
    <p class="mt-4 text-body text-muted">
      Jump to {#each sections as [id, label], i (id)}{#if i > 0}{' · '}{/if}<a class="link" href="#{id}">{label}</a>{/each}
    </p>

    <!-- Buttons -->
    <section id="buttons" class="mt-10" aria-labelledby="buttons-title">
      <h2 id="buttons-title" class="section-title">Buttons</h2>

      <p class="mt-4 mb-2 text-small text-muted">Variants (a real view has at most one filled button)</p>
      <div class="flex flex-wrap items-center gap-3">
        <Button variant="primary">Save</Button>
        <Button>Cancel</Button>
        <Button variant="ghost">Edit</Button>
        <Button variant="quiet">Show more</Button>
        <Button variant="danger">Delete file</Button>
        <Button variant="danger-text">Remove</Button>
      </div>

      <p class="mt-6 mb-2 text-small text-muted">Small, with an icon, as a link</p>
      <div class="flex flex-wrap items-center gap-3">
        <Button variant="primary" size="sm">Approve deploy</Button>
        <Button size="sm">Deny</Button>
        <Button icon="plus">New schedule</Button>
        <Button variant="ghost" icon="refresh" size="sm">Read again</Button>
        <Button href="/chat">Go to chat</Button>
        <Button href="https://github.com" external variant="ghost">View on GitHub</Button>
      </div>

      <p class="mt-6 mb-2 text-small text-muted">Loading keeps its width; disabled says why</p>
      <div class="flex flex-wrap items-center gap-3">
        <Button variant="primary" loading={saving} loadingLabel="Saving…" onclick={save}>Save changes</Button>
        <Button loading loadingLabel="Checking…">Check again</Button>
      </div>
      <div class="mt-3 flex flex-wrap items-center gap-3">
        <Button variant="primary" reason="Connect a model to continue.">Continue</Button>
      </div>

      <p class="mt-6 mb-2 text-small text-muted">Icon buttons: tooltip on hover or keyboard focus</p>
      <div class="flex flex-wrap items-center gap-2">
        <IconButton icon="search" label="Search" />
        <IconButton icon="paperclip" label="Attach files" variant="quiet" />
        <IconButton icon="copy" label="Copy" size="sm" variant="quiet" />
        <IconButton icon="refresh" label="Try again" variant="secondary" />
        <IconButton icon="menu" label="Open menu" pressed={navOpen} onclick={() => (navOpen = !navOpen)} />
        <IconButton icon="close" label="Close" disabled />
      </div>

      <p class="mt-6 mb-2 text-small text-muted">Link buttons and toggles</p>
      <div class="flex flex-wrap items-center gap-4">
        <p class="text-body">Connected. Bothy will use Claude Sonnet 4.5. <LinkButton onclick={() => {}}>Change</LinkButton></p>
        <LinkButton disabled>Replace</LinkButton>
        <ToggleButton label="Refine" icon="refresh" tooltip="Bothy drafts, checks and improves its answer before sending" bind:pressed={refine} />
        <ToggleButton label="Refine" bind:pressed={refineOn} />
      </div>
    </section>

    <!-- Fields -->
    <section id="fields" class="mt-10" aria-labelledby="fields-title">
      <h2 id="fields-title" class="section-title">Fields</h2>
      <div class="mt-4 grid gap-6 sm:grid-cols-2">
        <TextField label="Name" bind:value={name} placeholder="Alex Kim" help="Shown on approvals and in the activity log." autocomplete="name" />
        <TextField label="Daily limit" bind:value={limit} prefix="$" inputmode="decimal" />
        <TextField label="Username" value="alex kim" error="Use letters, numbers, dots and dashes only." />
        <TextField label="Team name" optional value="" placeholder="Acme" />
        <TextField label="Password" type={showPassword ? 'text' : 'password'} bind:value={password} help="12 more characters needed." autocomplete="new-password">
          {#snippet labelAction()}<LinkButton onclick={() => (showPassword = !showPassword)}>{showPassword ? 'Hide' : 'Show'}</LinkButton>{/snippet}
        </TextField>
        <TextField label="Server address" value="https://bothy.acme.dev" readonly />
        <TextField label="Region" value="eu-west-1" disabled help="Set by the server environment." />
        <Select
          label="AWS region"
          bind:value={region}
          options={[
            { value: 'us-east-1', label: 'US East (N. Virginia)' },
            { value: 'eu-west-1', label: 'Europe (Ireland)' },
            { value: 'ap-southeast-2', label: 'Asia Pacific (Sydney)' },
          ]}
        />
      </div>
      <div class="mt-6 grid gap-6">
        <TextArea label="Personality" bind:value={persona} maxRows={8} help="How Bothy talks to your team." />
        <TextArea label="Repeat rule" bind:value={rule} code rows={1} error="This repeat rule isn't valid. Use five parts, like 0 9 * * 1-5." />
      </div>

      <p class="mt-8 mb-2 text-small text-muted">Searchable choices</p>
      <div class="grid gap-6 sm:grid-cols-2">
        <Combobox label="Time zone" bind:value={zone} options={zones} />
        <Combobox label="Approvers" bind:value={people} options={team} multiple placeholder="Add people" help="Any one of them can approve." />
      </div>

      <p class="mt-8 mb-2 text-small text-muted">Key field: idle, checking, connected, failed, saved</p>
      <div class="grid gap-6">
        <KeyField label="Anthropic API key" bind:value={keyIdle} check={connects}>
          {#snippet help()}Create one in the <a class="link" href="https://console.anthropic.com/settings/keys" target="_blank" rel="noopener noreferrer">Anthropic console ↗</a>{/snippet}
        </KeyField>
        <KeyField label="Anthropic API key" bind:value={keyChecking} check={never} bind:this={kfChecking} />
        <KeyField label="Anthropic API key" bind:value={keyConnected} check={connects} bind:this={kfConnected} connectedAction={{ label: 'Change', onclick: () => {} }} />
        <KeyField label="Anthropic API key" bind:value={keyFailed} check={rejects} bind:this={kfFailed} />
        <KeyField label="OpenAI API key" check={connects} saved />
      </div>
    </section>

    <!-- Choices -->
    <section id="choices" class="mt-10" aria-labelledby="choices-title">
      <h2 id="choices-title" class="section-title">Choices</h2>
      <div class="mt-4 grid gap-6 sm:grid-cols-2">
        <div>
          <Checkbox label="Email me when something needs my approval" bind:checked={notify} />
          <Checkbox label="Weekly summary" description="A short note on Monday about what Bothy did." bind:checked={weekly} />
          <Checkbox label="Send to everyone" description="Only the owner can change this." checked disabled />
        </div>
        <fieldset>
          <legend class="field-label">Who can approve</legend>
          <Radio name="approver" value="admins" label="Any admin" bind:group={approver} />
          <Radio name="approver" value="people" label="Chosen people" description="Pick them after saving." bind:group={approver} />
          <Radio name="approver" value="owner" label="Only the owner" disabled bind:group={approver} />
        </fieldset>
      </div>

      <p class="mt-8 mb-2 text-small text-muted">Switches</p>
      <div class="flex flex-col items-start gap-1">
        <Switch label="Refine answers" bind:checked={switchOn} />
        <Switch label="Check-in" bind:checked={switchOff} />
        <Switch label="Running code" reason="Off on this server." />
      </div>

      <p class="mt-8 mb-2 text-small text-muted">Segmented</p>
      <Segmented
        label="Period"
        bind:value={period}
        options={[
          { value: '7', label: '7 days' },
          { value: '30', label: '30 days' },
        ]}
      />

      <p class="mt-8 mb-2 text-small text-muted">Option list, with the chosen option's extra controls</p>
      <OptionList
        name="preset"
        label="When should Bothy ask?"
        bind:value={preset}
        options={[
          { value: 'careful', title: 'Careful', description: 'Asks before changing files, sending anything or spending money.' },
          { value: 'balanced', title: 'Balanced', description: 'Asks before anything that leaves your team or can’t be undone.' },
          { value: 'hands-off', title: 'Hands-off', description: 'Asks only for deploys and payments.', disabled: true, reason: 'Turn on running code first.' },
        ]}
      >
        {#snippet expanded()}
          <Select
            label="Approvals expire after"
            bind:value={customHours}
            options={[
              { value: '1', label: '1 hour' },
              { value: '2', label: '2 hours' },
              { value: '24', label: '1 day' },
            ]}
          />
        {/snippet}
      </OptionList>
    </section>

    <!-- Navigation -->
    <section id="navigation" class="mt-10" aria-labelledby="navigation-title">
      <h2 id="navigation-title" class="section-title">Menus, lists and tabs</h2>

      <p class="mt-4 mb-2 text-small text-muted">Menu and model picker</p>
      <div class="flex flex-wrap items-center gap-3">
        <Menu items={chatItems}>
          {#snippet trigger(props)}<IconButton icon="more" label="Chat options" {...props} />{/snippet}
        </Menu>
        <Listbox bind:value={model} options={models} label="Model">
          {#snippet trigger(props, selected)}
            <button type="button" class="btn btn-ghost btn-sm" {...props} aria-label="Model: {selected?.label}">
              {selected?.label}<Icon name="chevron-down" class="text-muted" />
            </button>
          {/snippet}
        </Listbox>
        {#if lastPick}<p class="text-small text-muted" aria-live="polite">Chose “{lastPick}”.</p>{/if}
      </div>

      <p class="mt-8 mb-2 text-small text-muted">Tabs</p>
      <Tabs
        current="/dev/ui?tab=waiting"
        tabs={[
          { label: 'Waiting', href: '/dev/ui?tab=waiting', count: 2 },
          { label: 'Done', href: '/dev/ui?tab=done' },
          { label: 'For you', href: '/dev/ui?tab=you', count: 1204 },
        ]}
      />

      <p class="mt-8 mb-2 text-small text-muted">Disclosures</p>
      <div class="grid gap-4">
        <div>
          <p class="text-body">Anthropic had a problem answering.</p>
          <Disclosure bind:open={detailsOpen}>
            <CodeBlock code={'overloaded_error: Overloaded (529)\nrequest id req_011CXv4aPq'} wrap />
            <Disclosure label="Raw record" class="mt-2">
              <CodeBlock code={'{"type":"error","error":{"type":"overloaded_error"}}'} wrap />
            </Disclosure>
          </Disclosure>
        </div>
        <div class="text-body">
          File notes/staging.md · 3 lines ·
          <Disclosure variant="inline" label="Show">
            <CodeBlock code={stagingNotes} copy={false} />
          </Disclosure>
        </div>
        <Disclosure variant="section" label="More options">
          <div class="grid gap-4 sm:grid-cols-2">
            <TextField label="Branch" value="main" />
            <TextField label="Timeout" value="15 min" />
          </div>
        </Disclosure>
      </div>
    </section>

    <!-- Overlays -->
    <section id="overlays" class="mt-10" aria-labelledby="overlays-title">
      <h2 id="overlays-title" class="section-title">Dialogs, drawers and toasts</h2>
      <div class="mt-4 flex flex-wrap items-center gap-3">
        <Button onclick={() => (confirmOpen = true)}>Open confirm dialog</Button>
        <Button onclick={() => (formOpen = true)}>Open form dialog</Button>
        <Button onclick={() => (reviewOpen = true)}>Open review dialog</Button>
        <Button onclick={() => (drawerOpen = true)}>Open drawer</Button>
        <Button onclick={() => (navOpen = true)}>Open left drawer</Button>
      </div>
      <div class="mt-3 flex flex-wrap items-center gap-3">
        <Button variant="ghost" onclick={() => toast('Saved.')}>Show a toast</Button>
        <Button variant="ghost" onclick={() => toast('Chat deleted.', { action: { label: 'Undo', onclick: () => toast('Chat restored.') } })}>Show a toast with an action</Button>
        <Tooltip text="Stops new work until an admin resumes it">
          <Button variant="ghost">Pause Bothy…</Button>
        </Tooltip>
      </div>
    </section>

    <!-- Status -->
    <section id="status" class="mt-10" aria-labelledby="status-title">
      <h2 id="status-title" class="section-title">Status</h2>

      <p class="mt-4 mb-2 text-small text-muted">Banners, only when work stops</p>
      <div class="grid gap-2">
        <Banner tone="warning" text="Bothy is paused. Nothing new will start until an admin resumes it." action={{ label: 'Resume', onclick: () => {} }} />
        <Banner tone="danger" text="Bothy's saved keys are locked, so it can't reach its models or connections." action={{ label: 'Unlock', onclick: () => {} }} />
        {#if banners.restart}
          <Banner tone="neutral" text="Restart Bothy to apply your changes." dismissible ondismiss={() => (banners.restart = false)} />
        {:else}
          <LinkButton class="text-small" onclick={() => (banners.restart = true)}>Show the restart banner again</LinkButton>
        {/if}
      </div>

      <p class="mt-8 mb-2 text-small text-muted">Notices</p>
      <div class="grid gap-2">
        <Notice text="Older messages were summarised to keep this chat going." />
        <Notice tone="warning" text="This chat has seen private information and content from outside your team." action={{ label: 'Continue in a clean chat', onclick: () => {} }} />
        <Notice tone="danger" text="Bothy couldn't read acme/web. GitHub said the repository doesn't exist." action={{ label: 'Try again', onclick: () => {} }} />
      </div>

      <p class="mt-8 mb-2 text-small text-muted">Status lines</p>
      <div class="grid gap-3">
        <StatusLine tone="progress" text="Checking access to Amazon Bedrock…" />
        <StatusLine tone="success" text="Connected. Bothy will use Claude Sonnet 4.5." action={{ label: 'Change', onclick: () => {} }} />
        <StatusLine tone="warning" text="Expires in less than a minute" />
        <StatusLine
          tone="danger"
          text="Bothy blocks addresses on your private network unless you allow them."
          action={{ label: 'Allow 127.0.0.1:11434', onclick: () => {} }}
          details={'dial tcp 127.0.0.1:11434: blocked: private address'}
        />
      </div>

      <p class="mt-8 mb-2 text-small text-muted">Spinners and spending</p>
      <div class="flex flex-wrap items-center gap-4 text-body text-muted">
        <span class="inline-flex items-center gap-2"><Spinner size={12} /> Working…</span>
        <span class="inline-flex items-center gap-2"><Spinner /> Reading acme/web…</span>
      </div>
      <div class="mt-4 grid gap-4 sm:grid-cols-3">
        {#each [{ v: 2.5, tone: 'default' as const, t: 'Today: $2.50 of $5.00' }, { v: 4.1, tone: 'warning' as const, t: 'Today: $4.10 of $5.00' }, { v: 5, tone: 'danger' as const, t: 'Today: $5.00 of $5.00' }] as p (p.t)}
          <div>
            <p class="mb-2 text-small text-muted tabular-nums">{p.t}</p>
            <ProgressBar label="Spending today" value={p.v} max={5} tone={p.tone} valueText={p.t} />
          </div>
        {/each}
      </div>

      <p class="mt-8 mb-2 text-small text-muted">Empty state</p>
      <EmptyState text="Nothing yet. Tasks Bothy works on, in chats or on a schedule, show up here." action={{ label: 'Start a chat', href: '/chat' }} />
      <EmptyState class="mt-6" text="It's just you so far." more="Add people so they get their own sign-in." action={{ label: 'Add person', variant: 'secondary', onclick: () => (formOpen = true) }} />
    </section>

    <!-- Rows and settings -->
    <section id="content" class="mt-10" aria-labelledby="content-title">
      <h2 id="content-title" class="section-title">Rows and settings</h2>

      <p class="mt-4 mb-2 text-small text-muted">List rows</p>
      <div>
        <ListRow href="/dev/ui#content" title="acme/web" description="Up to date" meta={['1,204 files', '2 h ago']} />
        <ListRow href="/dev/ui#content" title="acme/infrastructure-and-deployment-scripts-for-every-region" description="Reading… 40%" meta={['Just now']} selected />
        <ListRow title="Sam Patel" avatar="Sam Patel" description="Admin" meta={['Today: $1.20']} />
        <ListRow title="Release notes" description="Writes release notes from merged pull requests." meta={['Changed yesterday']}>
          {#snippet trailing()}<Switch label="Release notes" hideLabel bind:checked={skillOn} />{/snippet}
        </ListRow>
        <ListRow href="/dev/ui#content" title="Monday summary" meta={['Every Monday at 9:00', 'Next in 3 days']}>
          {#snippet trailing()}
            <Menu items={chatItems} placement="bottom-end">
              {#snippet trigger(props)}<IconButton icon="more" label="Monday summary options" size="sm" variant="quiet" {...props} />{/snippet}
            </Menu>
          {/snippet}
        </ListRow>
      </div>

      <p class="mt-8 mb-2 text-small text-muted">Settings rows</p>
      <div>
        <SettingsSection title="Spending">
          <SettingsRow label="Daily limit" help="New work waits until midnight once it's reached." status={rowStatus}>
            {#snippet children(c)}
              <TextField id={c.id} aria-describedby={c.describedBy} prefix="$" inputmode="decimal" bind:value={rowLimit} onblur={saveRow} onkeydown={(e) => e.key === 'Enter' && saveRow()} />
            {/snippet}
          </SettingsRow>
          <SettingsRow label="Time zone">
            {#snippet children(c)}
              <Select id={c.id} bind:value={rowZone} options={zones} onchange={saveRow} />
            {/snippet}
          </SettingsRow>
          <SettingsRow label="Refine answers" inline>
            {#snippet children(c)}<Switch id={c.id} label="Refine answers" hideLabel bind:checked={refineRow} />{/snippet}
          </SettingsRow>
          <SettingsRow label="Public address" error="Couldn't save. Use a full address, like https://bothy.acme.dev." help="Where links in chat apps point.">
            {#snippet children(c)}<input id={c.id} class="input" value="bothy.acme" aria-invalid={c.invalid} aria-describedby={c.describedBy} />{/snippet}
          </SettingsRow>
        </SettingsSection>
      </div>

      <p class="mt-8 mb-2 text-small text-muted">Card</p>
      <Card>
        <p class="text-heading text-text">Delete notes/staging.md?</p>
        <p class="mt-1 text-body text-muted">This can't be undone.</p>
        <div class="mt-4 flex flex-wrap gap-2 max-[480px]:flex-col max-[480px]:[&>*]:w-full">
          <Button variant="danger">Delete file</Button>
          <Button>Allow for this chat</Button>
          <Button variant="ghost">Deny</Button>
        </div>
        <p class="mt-4 text-small text-muted">From “Clean up staging notes” · Expires in 15 min</p>
      </Card>

      <p class="mt-8 mb-2 text-small text-muted">Counts, dots, people and keys</p>
      <div class="flex flex-wrap items-center gap-x-6 gap-y-3 text-body">
        <span class="inline-flex items-center gap-2">Approvals <Count n={2} /></span>
        <span class="inline-flex items-center gap-2">Releases <Count n={0} /><span class="text-small text-muted">(nothing at 0)</span></span>
        <span class="inline-flex items-center gap-2">Settings <Dot /></span>
        <span class="inline-flex items-center gap-2"><Avatar name="Alex Kim" /> Alex Kim</span>
        <span class="inline-flex items-center gap-2"><Avatar name="Priya Shah" size={32} /> Priya Shah</span>
        <span class="inline-flex items-center gap-2"><Kbd keys={['⌘', 'K']} /> Search</span>
        <span class="inline-flex items-center gap-2"><Kbd keys="?" /> Shortcuts</span>
      </div>
    </section>

    <!-- Code and files -->
    <section id="code" class="mt-10" aria-labelledby="code-title">
      <h2 id="code-title" class="section-title">Code and files</h2>

      <p class="mt-4 mb-2 text-small text-muted">Commands and code</p>
      <div class="grid gap-4">
        <CommandBlock label="Run this on the server" code="docker compose exec bothy bothy setup-token" />
        <CodeBlock code={'git switch -c release/2.3\ngit push -u origin release/2.3'} />
        <CodeBlock code={config} maxLines={5} lineNumbers />
      </div>

      <p class="mt-8 mb-2 text-small text-muted">Changes to code</p>
      <DiffView diff={releaseDiff} />

      <p class="mt-8 mb-2 text-small text-muted">New file and deleted file</p>
      <div class="grid gap-4">
        <DiffView diff={checklist} mode="new-file" path="notes/launch-checklist.md" />
        <DiffView diff={stagingNotes} mode="deleted-file" />
      </div>

      <p class="mt-8 mb-2 text-small text-muted">Changes to writing</p>
      <DiffView diff={memoryDiff} mode="prose" />

      <p class="mt-8 mb-2 text-small text-muted">Attached files</p>
      <div class="flex flex-wrap items-center gap-2">
        <FileToken name="launch-plan.pdf" size={245760} onremove={() => {}} />
        <FileToken name="screenshot-of-the-new-approvals-inbox-on-a-phone.png" size={1048576} state="uploading" onremove={() => {}} />
        <FileToken name="budget.xlsx" state="error" onremove={() => {}} />
        <FileToken name="notes.txt" size={1820} href="/dev/ui#code" />
      </div>
    </section>
  </div>
</Page>

<Dialog bind:open={confirmOpen} title="Delete notes/staging.md?" initialFocus="cancel">
  <p class="text-body text-text">Bothy will remove this file from acme/web. This can't be undone.</p>
  {#snippet footer({ close })}
    <Button data-cancel onclick={close}>Cancel</Button>
    <Button
      variant="danger"
      onclick={() => {
        close();
        toast('File deleted.');
      }}>Delete file</Button
    >
  {/snippet}
</Dialog>

<Dialog bind:open={formOpen} title="Add person" size="md" dirty={!!(newName || newUsername)} busy={adding}>
  <div class="grid gap-4">
    <TextField label="Name" bind:value={newName} autocomplete="off" />
    <TextField label="Username" bind:value={newUsername} autocomplete="off" help="They sign in with this and a one-time password." />
    <Checkbox label="Admin" description="Admins can change settings and approve anything." />
  </div>
  {#snippet footer({ close })}
    <Button onclick={close} disabled={adding}>Cancel</Button>
    <Button variant="primary" loading={adding} loadingLabel="Adding…" onclick={addPerson}>Add person</Button>
  {/snippet}
</Dialog>

<Dialog bind:open={reviewOpen} title="Review changes to docs/release.md" size="lg">
  <DiffView diff={releaseDiff} />
  {#snippet footer({ close })}
    <Button onclick={close}>Cancel</Button>
    <Button variant="primary" onclick={close}>Approve changes</Button>
  {/snippet}
</Dialog>

<Drawer bind:open={drawerOpen} title="Chat details">
  <ListRow title="Started by" meta={['Alex Kim']} />
  <ListRow title="Model" meta={['Claude Sonnet 4.5']} />
  <ListRow title="Spent" meta={['$0.42']} />
  <Disclosure label="Technical details" class="mt-4">
    <CodeBlock code={'chat 01JTRIP0000000000\nmodel claude-sonnet-4-5-20250929'} wrap />
  </Disclosure>
</Drawer>

<Drawer bind:open={navOpen} title="Bothy" side="left" width={300}>
  <nav aria-label="Gallery sections">
    {#each sections as [id, label] (id)}
      <a class="flex h-8 items-center rounded-md px-2 text-body text-text hover:bg-hover pointer-coarse:h-11" href="#{id}" onclick={() => (navOpen = false)}>{label}</a>
    {/each}
  </nav>
</Drawer>

<ToastHost />
<LiveRegion />
