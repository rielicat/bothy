<script lang="ts">
  import { tick, untrack } from 'svelte';
  import { Button, Combobox, Dialog, Disclosure, IconButton, LinkButton, Segmented, Select, StatusLine, TextField, toast } from '../../../components/ui';
  import { api } from '../../../lib/api/client';
  import type { User } from '../../../lib/api/types.gen';
  import { approverShortfall, envConfig, envErrors, saveConfig, type EnvForm, type Failure } from './connections';
  import FailureLine from './FailureLine.svelte';
  import type { ReleaseEnvironment, Workflow } from './types';

  let {
    open = $bindable(false),
    env = null,
    repos,
    users,
    taken,
    onsaved,
    ondelete,
  }: {
    open?: boolean;
    /** The environment to edit; null adds one. */
    env?: ReleaseEnvironment | null;
    /** Repositories Bothy may use, owner/name. */
    repos: string[];
    users: User[];
    /** Names of the other environments. */
    taken: string[];
    onsaved: () => void;
    ondelete: (name: string) => void;
  } = $props();

  let f = $state<EnvForm>({ name: '', repo: '', workflow: '', branches: 'main', approvers: ['admin'], required: 1, inputs: [] });
  let workflows = $state<Workflow[] | null>(null);
  let tried = $state(false);
  let busy = $state(false);
  let failure = $state<Failure | null>(null);

  function reset() {
    tried = false;
    failure = null;
    f = env
      ? {
          name: env.name,
          repo: env.repo,
          workflow: env.workflow,
          branches: env.ref_pattern.split(',').join(', '),
          approvers: env.approvers.map((a) => a.id),
          required: Math.max(env.required, 1),
          inputs: Object.entries(env.inputs ?? {}).map(([key, value]) => ({ key, value })),
        }
      : { name: '', repo: repos[0] ?? '', workflow: '', branches: 'main', approvers: ['admin'], required: 1, inputs: [] };
  }

  $effect(() => {
    if (open) untrack(reset);
  });

  const file = (w: Workflow) => w.path.split('/').pop() ?? w.path;

  // A picker of the repository's workflows; a text field takes the file name when GitHub lists none or can't be asked.
  $effect(() => {
    const repo = f.repo;
    if (!open || !repo) return;
    workflows = null;
    api.get<Workflow[]>(`/api/github/repos/${repo.split('/').map(encodeURIComponent).join('/')}/workflows`).then(
      (list) => {
        if (f.repo !== repo) return;
        workflows = list;
        if (!list.some((w) => file(w) === f.workflow)) f.workflow = file(list.find((w) => /deploy/i.test(w.name + w.path)) ?? list[0] ?? { name: '', path: '' });
      },
      () => (workflows = []),
    );
  });

  const errors = $derived(envErrors(f, taken));
  const shortfall = $derived(approverShortfall(f.approvers, f.required, users));
  const show = (k: keyof typeof errors) => (tried ? errors[k] : undefined);

  async function save(e: SubmitEvent) {
    e.preventDefault();
    const form = e.currentTarget as HTMLFormElement;
    tried = true;
    if (Object.values(errors).some(Boolean)) {
      await tick();
      form.querySelector<HTMLElement>('[aria-invalid="true"]')?.focus();
      return;
    }
    busy = true;
    const set: Record<string, unknown> = { [`releases.environments.${f.name}`]: envConfig(f) };
    if (env && env.name !== f.name) set[`releases.environments.${env.name}`] = null;
    failure = await saveConfig(set);
    busy = false;
    if (failure) {
      failure.message = "Bothy couldn't save this environment. Check the details and try again.";
      return;
    }
    open = false;
    toast('Environment saved.');
    onsaved();
  }
</script>

<Dialog bind:open title={env ? `Edit ${env.name}` : 'Add environment'} size="md" dirty {busy} cancel>
  <form id="env-form" class="space-y-5" onsubmit={save} novalidate>
    <TextField label="Name" bind:value={f.name} error={show('name')} placeholder="production" autocomplete="off" spellcheck="false" />
    <Select label="Repository" bind:value={f.repo} error={show('repo')} options={repos.map((r) => ({ value: r, label: r }))} />
    {#if workflows?.length}
      <Select
        label="Deploy workflow"
        bind:value={f.workflow}
        help="The GitHub Actions workflow Bothy starts."
        options={workflows.map((w) => ({ value: file(w), label: `${w.name} (${file(w)})` }))}
      />
    {:else}
      <TextField
        label="Deploy workflow"
        bind:value={f.workflow}
        error={show('workflow')}
        placeholder="deploy.yml"
        spellcheck="false"
        help="The GitHub Actions workflow Bothy starts."
      />
    {/if}
    <TextField
      label="Branches"
      bind:value={f.branches}
      error={show('branches')}
      spellcheck="false"
      help="Bothy can only deploy from these. Separate with commas; * means any."
    />
    <Combobox
      label="Who can approve"
      multiple
      bind:value={f.approvers}
      error={show('approvers')}
      placeholder="Type a name"
      options={[{ value: 'admin', label: 'Any admin' }, ...users.filter((u) => !u.disabled).map((u) => ({ value: u.id, label: u.display_name || u.username }))]}
    />
    <div>
      <p class="field-label" id="env-required">Approvals needed</p>
      <Segmented
        class="mt-2 [&>button]:min-w-11 [&>button]:justify-center"
        label="Approvals needed"
        value={String(f.required)}
        onchange={(v) => (f.required = Number(v))}
        options={[
          { value: '1', label: '1' },
          { value: '2', label: '2' },
        ]}
      />
      {#if f.required === 2}<p class="field-help">Two different people must approve, and neither can be the person who asked.</p>{/if}
      {#if shortfall}<StatusLine class="mt-2" tone="warning" text={shortfall} />{/if}
    </div>
    <Disclosure variant="section" label="Workflow inputs" open={f.inputs.length > 0}>
      <div class="space-y-3">
        {#each f.inputs as input, i (i)}
          <div class="grid grid-cols-[minmax(0,1fr)_minmax(0,1fr)_auto] items-end gap-2">
            <TextField label="Input" bind:value={input.key} placeholder="version" spellcheck="false" />
            <TextField label="Value" bind:value={input.value} spellcheck="false" />
            <IconButton icon="close" label="Remove input {input.key || i + 1}" variant="quiet" onclick={() => f.inputs.splice(i, 1)} />
          </div>
        {/each}
        <LinkButton onclick={() => f.inputs.push({ key: '', value: '' })}>Add an input</LinkButton>
      </div>
    </Disclosure>
    <FailureLine {failure} />
    {#if env}
      <Button variant="danger-text" class="-ml-4" onclick={() => ((open = false), ondelete(env.name))}>Delete environment…</Button>
    {/if}
  </form>
  {#snippet footer()}
    <!-- On phones the button shares the sheet's header bar with the title, so it says only "Save". -->
    <Button variant="primary" type="submit" form="env-form" loading={busy} loadingLabel="Saving…">Save <span class="max-sm:hidden">environment</span></Button>
  {/snippet}
</Dialog>
