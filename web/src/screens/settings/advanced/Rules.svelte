<script lang="ts">
  import AdvancedField from '../../../components/AdvancedField.svelte';
  import { Button, Dialog, FieldError, SettingsSection } from '../../../components/ui';
  import { config, saveErrorText } from '../../../lib/config.svelte';
  import RuleDialog from './RuleDialog.svelte';
  import { matchesOf, readRule, verdictLabel, whenParts, type Rule } from './rules';
  import TestRuleDialog from './TestRuleDialog.svelte';

  const classes: [string, string][] = [
    ['read', 'Reading'],
    ['local_write', 'Changing files'],
    ['external_send', 'Sending information out'],
    ['irreversible', "Actions that can't be undone"],
    ['financial', 'Spending money'],
    ['admin', 'Changing settings'],
  ];
  const overrideOptions = [
    { value: '', label: 'As the approval setting says' },
    { value: 'auto', label: 'Allow' },
    { value: 'ask', label: 'Ask' },
    { value: 'deny', label: 'Deny' },
  ];

  const paths = $derived((config.view?.entries ?? []).map((e) => e.path));
  const rules = $derived(
    config.keys('policy.rules').map((id) => {
      const r = readRule(id, (f) => config.value(`policy.rules.${id}.${f}`));
      return { ...r, match: { ...r.match, ...matchesOf(id, paths, (p) => config.value(p)) } };
    }),
  );

  let editing = $state<Rule | 'new' | null>(null);
  let testing = $state(false);
  let deleting = $state<Rule | null>(null);
  let deleteBusy = $state(false);
  let deleteError = $state('');

  async function remove() {
    if (!deleting) return;
    deleteBusy = true;
    const r = await config.save({ [`policy.rules.${deleting.id}`]: null });
    deleteBusy = false;
    if (!r.ok) {
      deleteError = saveErrorText(r, undefined, { technical: true });
      return;
    }
    deleting = null;
  }
</script>

<SettingsSection title="Rules">
  <p class="text-small text-muted">The first rule that matches decides.</p>
  {#if rules.length}
    <table class="mt-3 w-full text-left text-body max-sm:block">
      <thead class="max-sm:sr-only">
        <tr class="border-b border-border text-small text-muted">
          <th scope="col" class="py-2 pr-4 font-normal">When</th>
          <th scope="col" class="py-2 pr-4 font-normal whitespace-nowrap">Bothy does</th>
          <th scope="col" class="py-2 pr-4 font-normal">Why</th>
          <th scope="col" class="py-2"><span class="sr-only">Edit</span></th>
        </tr>
      </thead>
      <tbody class="max-sm:block">
        {#each rules as r (r.id)}
          <tr class="border-b border-border align-top last:border-b-0 max-sm:grid max-sm:grid-cols-[minmax(0,1fr)_auto] max-sm:gap-x-3 max-sm:py-2">
            <td class="py-3 pr-4 max-sm:col-start-1 max-sm:py-0">
              <span class="font-mono text-code text-text">{r.tool || '*'}</span>
              <span class="block text-small text-muted [overflow-wrap:anywhere]">{r.id}{whenParts(r).length ? ` · ${whenParts(r).join(' · ')}` : ''}</span>
            </td>
            <td class="py-3 pr-4 whitespace-nowrap max-sm:col-start-1 max-sm:py-0 max-sm:text-small">{verdictLabel[r.verdict]}</td>
            <td class="py-3 pr-4 text-muted max-sm:col-start-1 max-sm:py-0 max-sm:text-small {r.reason ? '' : 'max-sm:hidden'}">{r.reason || '—'}</td>
            <td class="py-2 text-right max-sm:col-start-2 max-sm:row-span-3 max-sm:row-start-1 max-sm:py-0">
              <Button variant="ghost" size="sm" aria-label="Edit rule {r.id}" onclick={() => (editing = r)}>Edit</Button>
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  {:else}
    <p class="mt-3 text-body text-muted">No rules. The approval setting decides.</p>
  {/if}
  <div class="mt-3 flex flex-wrap gap-2">
    <Button onclick={() => (editing = 'new')}>Add rule</Button>
    <Button onclick={() => (testing = true)}>Test a rule…</Button>
  </div>

  <h3 class="mt-8 text-body font-medium text-text">For each kind of action</h3>
  <div class="mt-1">
    {#each classes as [c, label] (c)}
      <AdvancedField path="policy.defaults.{c}" {label} type="select" options={overrideOptions} />
    {/each}
  </div>
</SettingsSection>

<RuleDialog bind:rule={editing} ondelete={(r) => ((editing = null), (deleting = r), (deleteError = ''))} existing={rules.map((r) => r.id)} />
<TestRuleDialog bind:open={testing} />

<Dialog open={!!deleting} title="Delete rule {deleting?.id ?? ''}?" initialFocus="cancel" busy={deleteBusy} onclose={() => (deleting = null)} cancel>
  <p class="text-body text-text">Bothy goes back to the approval setting for these actions. This can't be undone.</p>
  <FieldError text={deleteError} />
  {#snippet footer()}
    <Button variant="danger" loading={deleteBusy} loadingLabel="Deleting…" onclick={remove}>Delete rule</Button>
  {/snippet}
</Dialog>
