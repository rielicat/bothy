<script lang="ts">
  import type { ModelInfo } from '../lib/api/types.gen';
  import { config } from '../lib/config.svelte';
  import { tokens } from '../lib/format';
  import { windowNote } from '../lib/providers';
  import Icon from './Icon.svelte';
  import SourceChip from './SourceChip.svelte';

  let { path, label, help = '', models }: { path: string; label: string; help?: string; models: ModelInfo[] } = $props();

  const chain = $derived(config.value<string[]>(path, []) ?? []);
  const info = (ref: string) => models.find((m) => `${m.ref.provider}/${m.ref.model}` === ref);
  const spare = $derived([...new Set(models.map((m) => `${m.ref.provider}/${m.ref.model}`))].filter((r) => !chain.includes(r)));
  const locked = $derived(!!config.entry(path)?.locked);
  let drag = -1;

  function save(next: string[]) {
    void config.set({ [path]: next }, true);
  }

  function move(i: number, d: number) {
    const next = [...chain];
    const [x] = next.splice(i, 1);
    next.splice(Math.max(0, Math.min(next.length, i + d)), 0, x);
    save(next);
  }
</script>

<div class="py-4">
  <div class="mb-2 flex flex-wrap items-center gap-2">
    <p class="text-sm font-medium">{label}</p>
    <SourceChip entry={config.entry(path)} />
  </div>
  {#if help}<p class="help -mt-1 mb-2">{help}</p>{/if}
  <ol class="space-y-1.5">
    {#each chain as ref, i (i)}
      {@const m = info(ref)}
      <li
        class="flex items-center gap-2 rounded-lg border border-line bg-surface px-2 py-1.5 text-sm"
        draggable={!locked}
        ondragstart={() => (drag = i)}
        ondragover={(e) => e.preventDefault()}
        ondrop={() => drag >= 0 && drag !== i && move(drag, i - drag)}
      >
        <Icon name="grip" size={15} class="cursor-grab text-faint" />
        <span class="w-14 text-xs text-muted">{i === 0 ? 'first' : 'fallback'}</span>
        <span class="mono min-w-0 flex-1 truncate">{ref}</span>
        {#if m?.context}<span class="text-xs text-muted">{tokens(m.context)}{windowNote(m.context) ? ` · ${windowNote(m.context)}` : ''}</span>{/if}
        {#if !locked}
          <button type="button" class="btn btn-ghost btn-icon btn-sm" aria-label="Move {ref} up" disabled={i === 0} onclick={() => move(i, -1)}><Icon name="up" size={14} /></button>
          <button type="button" class="btn btn-ghost btn-icon btn-sm" aria-label="Move {ref} down" disabled={i === chain.length - 1} onclick={() => move(i, 1)}><Icon name="down" size={14} /></button>
          <button type="button" class="btn btn-ghost btn-icon btn-sm" aria-label="Remove {ref}" onclick={() => save(chain.filter((r) => r !== ref))}><Icon name="x" size={14} /></button>
        {/if}
      </li>
    {:else}
      <li class="rounded-lg border border-dashed border-line px-3 py-3 text-sm text-muted">No model yet.</li>
    {/each}
  </ol>
  {#if !locked && spare.length}
    <select class="input mt-2 w-auto text-sm" aria-label="Add a model to {label}" value="" onchange={(e) => e.currentTarget.value && save([...chain, e.currentTarget.value])}>
      <option value="">{chain.length ? 'Add a fallback…' : 'Pick a model…'}</option>
      {#each spare as r (r)}<option value={r}>{r}</option>{/each}
    </select>
  {/if}
</div>
