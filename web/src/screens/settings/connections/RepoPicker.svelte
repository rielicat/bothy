<script lang="ts">
  import Icon from '../../../components/Icon.svelte';
  import { Checkbox, LinkButton } from '../../../components/ui';

  let {
    repos,
    selected = $bindable([]),
    card = false,
  }: {
    repos: { owner: string; name: string; description?: string }[];
    selected?: string[];
    /** On a page the choices sit on a group card; a dialog is a card already. */
    card?: boolean;
  } = $props();

  let q = $state('');
  const full = (r: { owner: string; name: string }) => `${r.owner}/${r.name}`;
  const shown = $derived.by(() => {
    const t = q.trim().toLowerCase();
    return t ? repos.filter((r) => full(r).toLowerCase().includes(t) || r.description?.toLowerCase().includes(t)) : repos;
  });

  function toggle(name: string, on: boolean) {
    selected = on ? [...selected, name] : selected.filter((s) => s !== name);
  }
</script>

<div class="relative">
  <Icon name="search" class="pointer-events-none absolute top-1/2 left-3 -translate-y-1/2 text-muted" />
  <input class="input pl-9 [&::-webkit-search-cancel-button]:appearance-none" type="search" placeholder="Filter repositories" aria-label="Filter repositories" bind:value={q} />
</div>
<div class="mt-2 {card ? 'list-card [&>label]:border-b [&>label]:border-border [&>label]:py-3' : ''}" data-copy-ignore>
  {#each shown as r (full(r))}
    <Checkbox label={full(r)} description={r.description} checked={selected.includes(full(r))} onchange={(e) => toggle(full(r), e.currentTarget.checked)} />
  {:else}
    <p class="py-2 text-body text-muted">No repositories match “{q.trim()}”.</p>
  {/each}
</div>
<p class="mt-1 text-small text-muted">
  <LinkButton onclick={() => (selected = [...new Set([...selected, ...shown.map(full)])])}>Select all</LinkButton>
  ·
  <LinkButton onclick={() => (selected = selected.filter((s) => !shown.some((r) => full(r) === s)))}>Clear</LinkButton>
</p>
