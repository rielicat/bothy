<script lang="ts">
  import { Button, Dialog, FieldError, Select, SettingsSection, TextField } from '../../../components/ui';
  import { api } from '../../../lib/api/client';
  import type { ModelInfo } from '../../../lib/api/types.gen';
  import { config, saveErrorText } from '../../../lib/config.svelte';
  import { focusInvalid } from '../security/page.svelte';

  const uid = $props.id();
  let models = $state<ModelInfo[]>([]);
  // ref being edited; '' for a new override, null when closed.
  let editing = $state<string | null>(null);
  let pick = $state('');
  let input = $state('');
  let output = $state('');
  let tried = $state(false);
  let busy = $state(false);
  let error = $state('');

  $effect(() => {
    api.get<ModelInfo[]>('/api/models').then((m) => (models = m ?? []), () => {});
  });

  const refOf = (m: ModelInfo) => `${m.ref.provider}/${m.ref.model}`;
  const name = (ref: string) => models.find((m) => refOf(m) === ref)?.display ?? ref.slice(ref.indexOf('/') + 1);
  const overrides = $derived(config.keys('pricing', 'tail'));
  const price = (ref: string, f: 'in' | 'out') => Number(config.value(`pricing.${ref}.${f}`, 0));
  const dollars = (n: number) => (n === 0 ? 'Free' : `$${n.toFixed(2)}`);
  const choices = $derived(models.map(refOf).filter((r) => !overrides.includes(r)));

  function open(ref: string) {
    editing = ref;
    pick = ref || choices[0] || '';
    input = ref ? String(price(ref, 'in')) : '';
    output = ref ? String(price(ref, 'out')) : '';
    tried = false;
    error = '';
  }

  const num = (s: string) => {
    const n = Number(s.trim().replace(/^\$/, ''));
    return s.trim() !== '' && Number.isFinite(n) && n >= 0 ? n : null;
  };
  const inError = $derived(num(input) === null ? 'Enter a price of 0 or more.' : '');
  const outError = $derived(num(output) === null ? 'Enter a price of 0 or more.' : '');

  async function save(e: SubmitEvent) {
    e.preventDefault();
    tried = true;
    const ref = editing || pick;
    if (inError || outError || !ref) return focusInvalid(e.currentTarget);
    busy = true;
    const r = await config.save({ [`pricing.${ref}`]: { in: num(input), out: num(output) } });
    busy = false;
    if (!r.ok) {
      error = saveErrorText(r, undefined, { technical: true });
      return;
    }
    editing = null;
  }
</script>

<SettingsSection title="Model prices">
  <p class="text-small text-muted">In dollars per million tokens.</p>
  {#if overrides.length}
    <!-- Under 640 px each row folds into name, id and one price line, like Rules. -->
    <table class="mt-3 w-full text-left text-body max-sm:block">
      <thead class="max-sm:sr-only">
        <tr class="border-b border-border text-small text-muted">
          <th scope="col" class="py-2 pr-4 font-normal">Model</th>
          <th scope="col" class="py-2 pr-4 text-right font-normal">Input per million</th>
          <th scope="col" class="py-2 pr-4 text-right font-normal">Output per million</th>
          <th scope="col" class="py-2"><span class="sr-only">Edit</span></th>
        </tr>
      </thead>
      <tbody class="max-sm:block">
        {#each overrides as ref (ref)}
          <tr class="border-b border-border last:border-b-0 max-sm:grid max-sm:grid-cols-[minmax(0,1fr)_auto] max-sm:items-center max-sm:gap-x-3 max-sm:py-2">
            <td class="py-2 pr-4 max-sm:col-start-1 max-sm:py-0">
              <span class="text-text">{name(ref)}</span>
              <span class="block font-mono text-code text-muted [overflow-wrap:anywhere]">{ref}</span>
            </td>
            <td class="py-2 pr-4 text-right tabular-nums max-sm:col-start-1 max-sm:py-0 max-sm:text-left max-sm:text-small max-sm:text-muted">
              <span class="sm:hidden">{'Input '}</span>{dollars(price(ref, 'in'))}<span class="sm:hidden">{`\u00a0· Output ${dollars(price(ref, 'out'))}`}</span>
            </td>
            <td class="py-2 pr-4 text-right tabular-nums max-sm:hidden">{dollars(price(ref, 'out'))}</td>
            <td class="py-2 text-right max-sm:col-start-2 max-sm:row-span-2 max-sm:row-start-1 max-sm:py-0">
              <Button variant="ghost" size="sm" aria-label="Edit prices for {name(ref)}" onclick={() => open(ref)}>Edit</Button>
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  {:else}
    <p class="mt-3 text-body text-muted">No price changes. Bothy uses its built-in prices.</p>
  {/if}
  {#if choices.length}
    <Button class="mt-3" onclick={() => open('')}>Add a price</Button>
  {/if}
</SettingsSection>

<Dialog open={editing !== null} title={editing ? `Prices for ${name(editing)}` : 'Add a price'} size="md" {busy} onclose={() => (editing = null)} cancel>
  <form id="{uid}-price" class="space-y-5" onsubmit={save} novalidate>
    {#if editing === ''}
      <Select label="Model" bind:value={pick} options={choices.map((r) => ({ value: r, label: `${name(r)} (${r})` }))} />
    {/if}
    <div class="grid gap-5 sm:grid-cols-2">
      <TextField label="Input per million" prefix="$" inputmode="decimal" bind:value={input} error={tried ? inError : ''} />
      <TextField label="Output per million" prefix="$" inputmode="decimal" bind:value={output} error={tried ? outError : ''} />
    </div>
    <FieldError text={error} />
  </form>
  {#snippet footer()}
    <Button variant="primary" type="submit" form="{uid}-price" loading={busy} loadingLabel="Saving…">Save prices</Button>
  {/snippet}
</Dialog>
