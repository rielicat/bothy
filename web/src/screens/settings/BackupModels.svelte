<script lang="ts">
  import { Disclosure, IconButton, Menu, Select, type MenuItem } from '../../components/ui';
  import { rowStatus } from '../../components/ui/saved.svelte';
  import type { ModelOption } from '../../components/ModelDialog.svelte';
  import { addBackup, moveBackup, removeAt } from './modelHelpers';
  import { Saver, settingsConfig } from './store.svelte';

  // The models each slot falls back to, in order (§11.4).
  let { options, label }: { options: ModelOption[]; label: (ref: string) => { name: string; provider: string } } = $props();

  const slots = [
    { path: 'models.main', title: 'For the main model', add: 'Add a backup for the main model' },
    { path: 'models.fast', title: 'For quick tasks', add: 'Add a backup for quick tasks' },
  ];
  const savers: Record<string, Saver> = { 'models.main': new Saver(), 'models.fast': new Saver() };
  let adding = $state<Record<string, string>>({ 'models.main': '', 'models.fast': '' });

  const chain = (path: string) => settingsConfig.value<string[]>(path, []) ?? [];

  function save(path: string, next: string[]) {
    return savers[path].run(() => settingsConfig.save({ [path]: next }));
  }

  function items(path: string, i: number, n: number): MenuItem[] {
    const c = chain(path);
    return [
      { label: 'Move up', disabled: i <= 1, reason: "It's already first.", onselect: () => void save(path, moveBackup(c, i, -1)) },
      { label: 'Move down', disabled: i >= n - 1, reason: "It's already last.", onselect: () => void save(path, moveBackup(c, i, 1)) },
      'separator',
      { label: 'Remove', onselect: () => void save(path, removeAt(c, i)) },
    ];
  }

  async function add(path: string, ref: string) {
    adding[path] = ref;
    if (ref) await save(path, addBackup(chain(path), ref));
    adding[path] = '';
  }
</script>

<Disclosure label="Backup models" variant="section" class="mt-2">
  <p class="text-small text-muted">If a model doesn't respond, Bothy tries these in order.</p>
  {#each slots as s (s.path)}
    {@const c = chain(s.path)}
    {@const saver = savers[s.path]}
    {@const free = options.filter((o) => !c.includes(o.value))}
    <div class="mt-5">
      <div class="flex flex-wrap items-baseline gap-x-2">
        <h3 class="text-body font-medium text-text">{s.title}</h3>
        <span class="text-small text-muted" aria-live="polite">{rowStatus(saver.status)}</span>
      </div>
      <ol class="mt-1 list-card">
        {#each c.slice(1) as ref, k (ref)}
          {@const m = label(ref)}
          <li class="flex min-h-10 items-center gap-3 border-b border-border py-1 pointer-coarse:min-h-12">
            <span class="w-4 shrink-0 text-small text-muted tabular-nums">{k + 1}</span>
            <span class="min-w-0 flex-1 truncate text-body text-text">{m.name} <span class="text-muted">· {m.provider}</span></span>
            <Menu items={items(s.path, k + 1, c.length)} placement="bottom-end">
              {#snippet trigger(props)}<IconButton icon="more" label="Options for {m.name}" size="sm" variant="quiet" {...props} />{/snippet}
            </Menu>
          </li>
        {:else}
          <li class="py-3 text-body text-muted">No backups yet.</li>
        {/each}
      </ol>
      {#if saver.error}<p class="field-error" role="alert">{saver.error}</p>{/if}
      {#if free.length}
        <Select
          class="mt-2 sm:max-w-80"
          label={s.add}
          hideLabel
          value={adding[s.path]}
          options={[{ value: '', label: 'Add backup' }, ...free.map((o) => ({ value: o.value, label: `${o.label} · ${o.group}` }))]}
          onchange={(e: Event) => add(s.path, (e.currentTarget as HTMLSelectElement).value)}
        />
      {/if}
    </div>
  {/each}
</Disclosure>
