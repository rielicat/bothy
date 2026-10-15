<script lang="ts">
  import Icon from '../Icon.svelte';
  import ModelDialog from '../ModelDialog.svelte';
  import Menu, { type MenuItem } from '../ui/Menu.svelte';
  import { models, providerName, refOf, shortName } from './models.svelte';

  // The chat's model (§7.5): '' follows the main model; anything else is this
  // chat's override. Hidden while only one model is connected.
  let { value, onchange }: { value: string; onchange: (v: string) => void } = $props();

  const SHORT = 8;
  const rest = $derived(models.list.filter((m) => refOf(m) !== models.main && refOf(m) !== models.fast));
  const options = $derived(
    rest
      .map((m) => ({ value: refOf(m), label: models.name(refOf(m)), group: providerName(m.ref.provider) }))
      .sort((a, b) => a.group.localeCompare(b.group) || a.label.localeCompare(b.label)),
  );
  let more = $state(false);

  const items = $derived.by(() => {
    const { main, fast } = models;
    const pick = (v: string) => () => v !== value && onchange(v);
    const out: MenuItem[] = [{ label: main ? `Main model · ${models.name(main)}` : 'Main model', checked: !value, onselect: pick('') }];
    if (fast) out.push({ label: `Quick tasks model · ${models.name(fast)}`, checked: value === fast, onselect: pick(fast) });
    // A router connects hundreds of models: the menu keeps to each provider's
    // suggestions and the chat's own choice, and the rest are a search away.
    const short = rest.length <= SHORT ? rest : rest.filter((m) => m.recommended || m.quick_tasks_default || refOf(m) === value);
    if (short.length) out.push('separator');
    for (const m of short) out.push({ label: models.name(refOf(m)), group: providerName(m.ref.provider), checked: value === refOf(m), onselect: pick(refOf(m)) });
    if (short.length < rest.length) out.push('separator', { label: 'More models…', onselect: () => (more = true) });
    return out;
  });
  const label = $derived(value ? models.name(value) : models.main ? models.name(models.main) : 'Main model');
</script>

{#if models.list.length > 1}
  <Menu {items} placement="top-end">
    {#snippet trigger(props)}
      <button type="button" class="btn btn-ghost btn-sm min-w-0 gap-1 px-2 text-small {value ? 'text-text' : 'text-muted'}" {...props} aria-label="Model: {label}">
        <span class="truncate max-[400px]:hidden">{label}</span><span class="truncate min-[400px]:hidden">{shortName(label)}</span>
        <Icon name="chevron-down" />
      </button>
    {/snippet}
  </Menu>
  <ModelDialog
    bind:open={more}
    title="Model for this chat"
    {value}
    {options}
    onsave={async (ref) => {
      if (ref !== value) onchange(ref);
      return true;
    }}
  />
{/if}
