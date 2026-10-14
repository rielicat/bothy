<script lang="ts">
  import Icon from '../Icon.svelte';
  import Menu, { type MenuItem } from '../ui/Menu.svelte';
  import { models, providerName, refOf, shortName } from './models.svelte';

  // The chat's model (§7.5): '' follows the main model; anything else is this
  // chat's override. Hidden while only one model is connected.
  let { value, onchange }: { value: string; onchange: (v: string) => void } = $props();

  const items = $derived.by(() => {
    const { main, fast } = models;
    const pick = (v: string) => () => v !== value && onchange(v);
    const out: MenuItem[] = [{ label: main ? `Main model · ${models.name(main)}` : 'Main model', checked: !value, onselect: pick('') }];
    if (fast) out.push({ label: `Quick tasks model · ${models.name(fast)}`, checked: value === fast, onselect: pick(fast) });
    const rest = models.list.filter((m) => refOf(m) !== main && refOf(m) !== fast);
    if (rest.length) out.push('separator');
    for (const m of rest) out.push({ label: models.name(refOf(m)), group: providerName(m.ref.provider), checked: value === refOf(m), onselect: pick(refOf(m)) });
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
{/if}
