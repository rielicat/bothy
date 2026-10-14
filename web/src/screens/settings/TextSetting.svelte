<script lang="ts">
  import { untrack } from 'svelte';
  import Icon from '../../components/Icon.svelte';
  import { LinkButton, SettingsRow, TextField } from '../../components/ui';
  import { helpFor, Saver, settingsConfig } from './store.svelte';

  // A text setting (§11.1): saves on blur or Enter, shows "Saved", keeps an
  // invalid value unsaved with the error under it.
  let {
    label,
    path,
    help,
    placeholder,
    prefix,
    inputmode,
    autocomplete = 'off',
    format = (v: unknown) => (v === null || v === undefined ? '' : String(v)),
    parse = (s: string) => ({ value: s.trim() as unknown }),
  }: {
    label: string;
    path: string;
    help?: string;
    placeholder?: string;
    prefix?: string;
    inputmode?: 'text' | 'decimal' | 'url' | 'email';
    autocomplete?: 'off' | 'url';
    format?: (v: unknown) => string;
    parse?: (s: string) => { value?: unknown; error?: string };
  } = $props();

  const saver = new Saver();
  const current = $derived(format(settingsConfig.value(path)));
  let draft = $state(untrack(() => current));
  // What the draft started from; a different value arriving while the draft differs is a conflict.
  let base = $state(untrack(() => current));
  let conflict = $state(false);
  const uid = $props.id();

  $effect(() => {
    const c = current;
    untrack(() => {
      if (c === base) return;
      if (draft === base) draft = base = c;
      else conflict = true;
    });
  });

  function reload() {
    draft = base = current;
    conflict = false;
    saver.clear();
  }

  async function commit() {
    if (conflict) return;
    const p = parse(draft);
    if (p.error) return saver.fail(p.error);
    if (draft === base) return saver.clear();
    const prev = base;
    base = draft;
    if (!(await saver.run(() => settingsConfig.save({ [path]: p.value })))) base = prev;
  }

  function typed() {
    if (saver.error && !parse(draft).error) saver.clear();
  }
</script>

<SettingsRow {label} help={helpFor(path, help)} status={saver.status} error={saver.error}>
  {#snippet children(c)}
    <TextField
      id={c.id}
      bind:value={draft}
      {placeholder}
      {prefix}
      {inputmode}
      {autocomplete}
      aria-invalid={c.invalid || conflict || undefined}
      aria-describedby={[c.describedBy, conflict ? `${uid}-conflict` : ''].filter(Boolean).join(' ') || undefined}
      oninput={typed}
      onblur={commit}
      onkeydown={(e: KeyboardEvent) => {
        if (e.key === 'Enter') {
          e.preventDefault();
          void commit();
        }
      }}
    />
    {#if conflict}
      <p id="{uid}-conflict" class="field-error">
        <Icon name="alert" class="mt-px" /><span class="min-w-0">Someone else changed this setting. Reload to see their change. <LinkButton onclick={reload}>Reload</LinkButton></span>
      </p>
    {/if}
  {/snippet}
</SettingsRow>
