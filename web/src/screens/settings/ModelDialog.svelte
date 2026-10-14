<script lang="ts" module>
  export interface ModelOption {
    value: string;
    label: string;
    group: string;
  }
</script>

<script lang="ts">
  import Icon from '../../components/Icon.svelte';
  import { Button, Dialog, Notice } from '../../components/ui';
  import { findTypeahead, typeahead } from '../../components/ui/layer';

  // Change a model slot (§11.4): connected models by friendly name, grouped by
  // provider, as a listbox that stays open in the dialog so one click chooses.
  let {
    open = $bindable(false),
    title,
    value,
    options,
    onsave,
  }: {
    open?: boolean;
    title: string;
    value: string;
    options: ModelOption[];
    /** Resolves false when the choice couldn't be saved. */
    onsave: (ref: string) => Promise<boolean>;
  } = $props();

  const uid = $props.id();
  let choice = $state('');
  let active = $state(0);
  let busy = $state(false);
  let failed = $state(false);
  const search = typeahead();

  const groups = $derived.by(() => {
    const out: { name: string; items: { o: ModelOption; i: number }[] }[] = [];
    options.forEach((o, i) => {
      const last = out[out.length - 1];
      if (last?.name === o.group) last.items.push({ o, i });
      else out.push({ name: o.group, items: [{ o, i }] });
    });
    return out;
  });
  const optId = (i: number) => `${uid}-opt-${i}`;

  $effect(() => {
    if (!open) return;
    choice = value;
    active = Math.max(0, options.findIndex((o) => o.value === value));
    failed = false;
  });

  function move(i: number) {
    active = Math.min(options.length - 1, Math.max(0, i));
    document.getElementById(optId(active))?.scrollIntoView({ block: 'nearest' });
  }

  function key(e: KeyboardEvent) {
    const map: Record<string, number> = { ArrowDown: active + 1, ArrowUp: active - 1, Home: 0, End: options.length - 1 };
    if (e.key in map) {
      e.preventDefault();
      move(map[e.key]);
    } else if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault();
      if (options[active]) choice = options[active].value;
      if (e.key === 'Enter') void save();
    } else if (e.key.length === 1 && !e.metaKey && !e.ctrlKey && !e.altKey) {
      const i = findTypeahead(
        options.map((o) => o.label),
        search(e.key),
        active,
      );
      if (i >= 0) move(i);
    }
  }

  async function save() {
    if (choice === value) {
      open = false;
      return;
    }
    busy = true;
    failed = false;
    const ok = await onsave(choice);
    busy = false;
    if (ok) open = false;
    else failed = true;
  }
</script>

<Dialog bind:open {title} size="md" {busy}>
  <div
    role="listbox"
    tabindex="0"
    aria-label={title}
    aria-activedescendant={options.length ? optId(active) : undefined}
    class="-mx-2 rounded-md focus-visible:outline-offset-0"
    onkeydown={key}
  >
    {#each groups as g, gi (g.name)}
      <div role="group" aria-labelledby="{uid}-g{gi}">
        <p id="{uid}-g{gi}" class="px-2 pb-1 text-caption text-muted {gi ? 'pt-3' : 'pt-1'}">{g.name}</p>
        {#each g.items as { o, i } (o.value)}
          {@const on = o.value === choice}
          <!-- svelte-ignore a11y_click_events_have_key_events -->
          <div
            id={optId(i)}
            role="option"
            tabindex="-1"
            aria-selected={on}
            class="flex min-h-8 cursor-pointer items-center gap-2 rounded-md px-2 text-body text-text pointer-coarse:min-h-11 {i === active ? 'bg-hover' : ''}"
            onpointermove={() => (active = i)}
            onclick={() => {
              active = i;
              choice = o.value;
            }}
          >
            <span class="flex w-4 shrink-0 justify-center">{#if on}<Icon name="check" />{/if}</span>
            <span class="min-w-0 truncate">{o.label}</span>
          </div>
        {/each}
      </div>
    {/each}
    {#if !options.length}<p class="px-2 py-1.5 text-body text-muted">No models are connected yet. Add a provider first.</p>{/if}
  </div>
  {#if failed}<Notice class="mt-3" tone="danger" text="Couldn't save. Try again." role="alert" />{/if}
  {#snippet footer({ close })}
    <Button data-cancel onclick={close} disabled={busy}>Cancel</Button>
    <Button variant="primary" loading={busy} loadingLabel="Saving…" onclick={save}>Save</Button>
  {/snippet}
</Dialog>
