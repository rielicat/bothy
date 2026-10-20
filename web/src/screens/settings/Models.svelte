<script lang="ts">
  import { untrack } from 'svelte';
  import ProviderChooser, { type Connected } from '../../components/ProviderChooser.svelte';
  import { Button, Dialog, SettingsRow, SettingsSection, StatusLine, toast } from '../../components/ui';
  import { api } from '../../lib/api/client';
  import type { ModelInfo, ProviderHealth, RefineInfo } from '../../lib/api/types.gen';
  import BackupModels from './BackupModels.svelte';
  import ItemRow from './ItemRow.svelte';
  import ManageProvider, { type ManagedProvider } from './ManageProvider.svelte';
  import ModelDialog, { type ModelOption } from '../../components/ModelDialog.svelte';
  import { makePrimary, modelName, modelProvider, providerName, providerStatus, splitRef } from './modelHelpers';
  import SettingsFrame from './SettingsFrame.svelte';
  import { Saver, settingsConfig } from './store.svelte';
  import SwitchSetting from './SwitchSetting.svelte';
  import ThoroughSetting from './ThoroughSetting.svelte';

  const uid = $props.id();
  let models = $state<ModelInfo[]>([]);
  let health = $state<ProviderHealth[]>([]);

  async function refresh() {
    const [m, h] = await Promise.all([api.get<ModelInfo[]>('/api/models').catch(() => models), api.get<ProviderHealth[]>('/api/providers').catch(() => health)]);
    models = m;
    health = h;
  }

  $effect(() => {
    untrack(() => void refresh());
  });

  const enabled = (id: string) => settingsConfig.value(`providers.${id}.enabled`, null) !== false;
  // Providers in use first, those turned off after them.
  const ids = $derived(settingsConfig.keys('providers').sort((a, b) => Number(enabled(b)) - Number(enabled(a))));
  const kinds = $derived(Object.fromEntries(ids.map((id) => [id, settingsConfig.value<string>(`providers.${id}.type`, '')])));

  const slots = $derived({
    'models.main': settingsConfig.value<string[]>('models.main', []) ?? [],
    'models.fast': settingsConfig.value<string[]>('models.fast', []) ?? [],
  });
  type Slot = keyof typeof slots;

  const label = (ref: string) => ({ name: modelName(ref, models), provider: modelProvider(ref, models, kinds) });

  // Connected models by provider, plus any model a slot names that the list doesn't (yet) report.
  const options: ModelOption[] = $derived.by(() => {
    const refs = models.filter((m) => ids.includes(m.ref.provider) && enabled(m.ref.provider)).map((m) => `${m.ref.provider}/${m.ref.model}`);
    for (const r of [...slots['models.main'], ...slots['models.fast']]) if (!refs.includes(r) && ids.includes(splitRef(r).provider)) refs.push(r);
    const order = (r: string) => ids.indexOf(splitRef(r).provider);
    const recommended = (r: string) => !!models.find((x) => `${x.ref.provider}/${x.ref.model}` === r)?.recommended;
    // Within a provider: recommended first, then by name, not by id.
    return refs
      .sort((a, b) => order(a) - order(b) || Number(recommended(b)) - Number(recommended(a)) || label(a).name.localeCompare(label(b).name))
      .map((r) => ({ value: r, label: label(r).name + (recommended(r) ? ' (recommended)' : ''), group: label(r).provider }));
  });

  const savers: Record<Slot, Saver> = { 'models.main': new Saver(), 'models.fast': new Saver() };
  let changing = $state<Slot | null>(null);
  let changeOpen = $state(false);

  async function saveSlot(path: Slot, ref: string): Promise<boolean> {
    try {
      await settingsConfig.save({ [path]: makePrimary(slots[path], ref) });
      savers[path].done();
      return true;
    } catch {
      return false;
    }
  }

  function managed(id: string): ManagedProvider {
    const kind = kinds[id];
    const name = providerName(id, kind);
    const inSlots = [...slots['models.main'], ...slots['models.fast']].find((r) => splitRef(r).provider === id);
    const model = inSlots ? splitRef(inSlots).model : (models.find((m) => m.ref.provider === id)?.ref.model ?? settingsConfig.keys(`providers.${id}.models`)[0] ?? '');
    const uses = (path: Slot) => splitRef(slots[path][0] ?? '').provider === id;
    const keep = settingsConfig.locked(`providers.${id}.id`)
      ? `${name} is set up in the server's settings file, so it can't be removed here.`
      : uses('models.main')
        ? `Bothy uses ${name} for the main model. Choose another main model first.`
        : uses('models.fast')
          ? `Bothy uses ${name} for quick tasks. Choose another quick tasks model first.`
          : undefined;
    return {
      id,
      kind,
      name,
      enabled: enabled(id),
      baseUrl: settingsConfig.value<string>(`providers.${id}.base_url`, ''),
      keyRef: settingsConfig.value<string>(`providers.${id}.api_key`, ''),
      allowPrivate: settingsConfig.value<unknown>(`providers.${id}.allow_private`) === true,
      api: settingsConfig.value<string>(`providers.${id}.api`, ''),
      region: settingsConfig.value<string>(`providers.${id}.region`, 'us-east-1'),
      health: health.find((h) => h.id === id),
      model,
      models: models.filter((m) => m.ref.provider === id).length,
      keep,
    };
  }

  let managing = $state<string | null>(null);
  let manageOpen = $state(false);
  const managedNow = $derived(managing ? managed(managing) : null);

  let adding = $state(false);
  let before = $state(new Set<string>());
  let chooser: ProviderChooser | undefined = $state();
  let addReady = $state(false);
  let addSaving = $state(false);

  function add() {
    before = new Set(ids);
    adding = true;
  }

  async function connected(p: Connected) {
    adding = false;
    await settingsConfig.load(true);
    void refresh();
    const added = ids.find((id) => !before.has(id));
    const name = p.name || (added ? providerName(added, kinds[added]) : '');
    toast(name ? `${name} connected.` : 'Connected.');
  }

  const refineOn = $derived(settingsConfig.value<unknown>('agent.refine.enabled') === true);
  // The server works out the cost from the drafts, rounds and early stop, so it is asked again after each change.
  let multiplier = $state(0);
  $effect(() => {
    void settingsConfig.value('agent.refine.drafts');
    void settingsConfig.value('agent.refine.depth');
    void api
      .get<RefineInfo>('/api/refine')
      .then((r) => (multiplier = Math.round(r.multiplier)))
      .catch(() => (multiplier = 0));
  });
</script>

{#snippet slotRow(path: Slot, title: string, help: string)}
  {@const ref = slots[path][0]}
  <SettingsRow label={title} {help} status={savers[path].status}>
    {#snippet children()}
      <div class="flex items-center justify-between gap-3">
        <span class="min-w-0 truncate text-body text-text">
          {#if ref}{label(ref).name} <span class="text-muted">· {label(ref).provider}</span>{:else}<span class="text-muted">Not chosen</span>{/if}
        </span>
        <Button
          size="sm"
          aria-label="Change {title.toLowerCase()}"
          onclick={() => {
            changing = path;
            changeOpen = true;
          }}>Change</Button
        >
      </div>
    {/snippet}
  </SettingsRow>
{/snippet}

<SettingsFrame title="Models" loading={!settingsConfig.view && !settingsConfig.failed}>
  {#if settingsConfig.failed}
    <StatusLine tone="danger" text="This page didn't load. Check your connection and try again." action={{ label: 'Try again', onclick: () => settingsConfig.load(true) }} />
  {:else if settingsConfig.view}
    <div>
      {@render slotRow('models.main', 'Main model', 'Used for chats and most work.')}
      {@render slotRow('models.fast', 'Quick tasks model', 'Used for titles, summaries and check-ins. A cheaper model works well.')}
    </div>
    <BackupModels {options} {label} />

    <section class="mt-10" aria-labelledby="{uid}-providers">
      <div class="flex items-center justify-between gap-4">
        <h2 id="{uid}-providers" class="section-title">Providers</h2>
        <Button size="sm" onclick={add}>Add a provider</Button>
      </div>
      <div class="mt-2">
        {#each ids as id (id)}
          {@const st = providerStatus({
            kind: kinds[id],
            enabled: enabled(id),
            health: health.find((h) => h.id === id),
            models: models.filter((m) => m.ref.provider === id).length,
            region: settingsConfig.value<string>(`providers.${id}.region`, ''),
          })}
          <ItemRow name={providerName(id, kinds[id])} status={st.text} tone={st.tone}>
            {#snippet trailing()}
              <Button
                size="sm"
                aria-label="Manage {providerName(id, kinds[id])}"
                onclick={() => {
                  managing = id;
                  manageOpen = true;
                }}>Manage</Button
              >
            {/snippet}
          </ItemRow>
        {:else}
          <p class="py-2 text-body text-muted">No providers yet. Add one so Bothy can answer chats.</p>
        {/each}
      </div>
    </section>

    {#if settingsConfig.has('agent.refine.enabled')}
      <SettingsSection title="Refine">
        <SwitchSetting
          label="Refine answers by default"
          path="agent.refine.enabled"
          help="Bothy writes several drafts, checks them and sends the best one. {multiplier > 1 ? `Slower, and costs about ${multiplier}× more.` : 'Slower, and costs several times more.'}"
        />
        {#if refineOn}<ThoroughSetting />{/if}
      </SettingsSection>
    {/if}
  {/if}
</SettingsFrame>

{#if changing}
  {@const path = changing}
  <ModelDialog
    bind:open={changeOpen}
    title={path === 'models.main' ? 'Main model' : 'Quick tasks model'}
    value={slots[path][0] ?? ''}
    {options}
    onsave={(ref) => saveSlot(path, ref)}
  />
{/if}

<ManageProvider bind:open={manageOpen} provider={managedNow} onchanged={refresh} />

<!-- Nothing is added until "Add provider", like setup's Continue; the dialog closes only then, with a toast. -->
<Dialog bind:open={adding} title="Add a provider" size="md" busy={addSaving} cancel lockCancel>
  <ProviderChooser bind:this={chooser} mode="settings" exclude={[...before]} onconnected={connected} bind:ready={addReady} bind:saving={addSaving} />
  {#if !addReady}<p id="{uid}-add-why" class="mt-4 text-small text-muted">Connect a provider to add it.</p>{/if}
  {#snippet footer()}
    <Button
      variant="primary"
      disabled={!addReady}
      aria-describedby={addReady ? undefined : `${uid}-add-why`}
      loading={addSaving}
      loadingLabel="Adding…"
      onclick={() => chooser?.save()}>Add provider</Button
    >
  {/snippet}
</Dialog>
