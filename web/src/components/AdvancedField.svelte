<script lang="ts">
  import { config, saveErrorText } from '../lib/config.svelte';
  import { parseGoDuration, prettyGoDuration } from '../lib/format';
  import { LinkButton, Select, SettingsRow, Switch, type SelectOption } from './ui';

  // A settings row bound to one config path, with the provenance Advanced
  // shows and nowhere else (docs/design/ui.md §11.7, §15.3 row 27).
  let {
    path,
    label,
    type = 'text',
    options = [],
    help,
    placeholder,
  }: {
    path: string;
    label: string;
    type?: 'text' | 'number' | 'money' | 'duration' | 'list' | 'select' | 'switch';
    options?: SelectOption[];
    help?: string;
    placeholder?: string;
  } = $props();

  const entry = $derived(config.entry(path));
  const schema = $derived(config.schema(path));
  // A path this server doesn't know (older server, feature not built) shows no row.
  const known = $derived(!!entry || !!schema);
  const env = $derived(entry?.source.layer === 'env' ? (entry.source.env_var ?? '') : null);
  const locked = $derived(!!entry?.locked);
  const readOnly = $derived(env !== null || locked);
  const changed = $derived(!readOnly && config.changed(path));
  const value = $derived(entry ? entry.value : schema?.default);
  const integer = $derived(schema?.type === 'integer' || (Array.isArray(schema?.type) && schema.type.includes('integer')));
  const help2 = $derived([help, entry && !entry.hot ? 'Takes effect after a restart.' : ''].filter(Boolean).join(' ') || undefined);

  function show(v: unknown): string {
    if (v === null || v === undefined) return '';
    if (type === 'duration') return prettyGoDuration(String(v));
    if (type === 'list') return Array.isArray(v) ? v.join(', ') : String(v);
    return String(v);
  }

  let draft = $state('');
  let editing = $state(false);
  let status = $state<'saving' | 'saved' | null>(null);
  let error = $state('');

  $effect(() => {
    const v = show(value);
    if (!editing) draft = v;
  });

  // Text kinds parse here; null means the draft is invalid and `error` says why.
  function parse(text: string): { ok: true; value: unknown } | { ok: false; error: string } {
    const t = text.trim();
    if (type === 'number' || type === 'money') {
      const n = Number(t.replace(/^\$/, '').replace(/,/g, ''));
      if (t === '' || !Number.isFinite(n)) return { ok: false, error: 'Enter a number.' };
      if (integer && !Number.isInteger(n)) return { ok: false, error: 'Enter a whole number.' };
      return { ok: true, value: n };
    }
    if (type === 'duration') {
      if (parseGoDuration(t) === null) return { ok: false, error: 'Enter a duration like 45s, 10m or 2h.' };
      return { ok: true, value: t };
    }
    if (type === 'list') {
      const items = t.split(/[,\n]/).map((s) => s.trim()).filter(Boolean);
      const numeric = schema?.items?.type === 'integer' || schema?.items?.type === 'number';
      if (numeric && items.some((s) => !Number.isFinite(Number(s)))) return { ok: false, error: 'Enter numbers separated by commas.' };
      return { ok: true, value: numeric ? items.map(Number) : items };
    }
    return { ok: true, value: t };
  }

  async function commit(next: unknown) {
    status = 'saving';
    error = '';
    const r = await config.save({ [path]: next });
    status = r.ok ? 'saved' : null;
    if (!r.ok) error = saveErrorText(r, path, { technical: true });
  }

  function commitText() {
    editing = false;
    if (draft === show(value)) return;
    const p = parse(draft);
    if (!p.ok) {
      error = p.error;
      return;
    }
    void commit(p.value);
  }

  // An empty option ("As the approval setting says") means no value of its own.
  function choose(v: string) {
    if (v === '' && options.some((o) => o.value === '')) void reset();
    else void commit(v);
  }

  async function reset() {
    status = 'saving';
    error = '';
    const r = await config.resetToDefault(path);
    status = r.ok ? 'saved' : null;
    if (!r.ok) error = saveErrorText(r, path, { technical: true });
  }

  const selectOptions = $derived(
    options.some((o) => o.value === String(value ?? '')) ? options : [...options, { value: String(value ?? ''), label: String(value ?? '') }],
  );
</script>

{#snippet provenance()}
  {#if env !== null}
    <span>Set by the server environment{env ? ` (${env})` : ''}. Change it there.</span>
  {:else if locked}
    <span>Locked on this server, so it can't be changed here.</span>
  {:else}
    <span>Changed · <LinkButton onclick={reset}>Reset to default</LinkButton></span>
  {/if}
{/snippet}

{#if known}
  <SettingsRow {label} help={help2} {status} {error} inline={type === 'switch'} note={readOnly || changed ? provenance : undefined}>
    {#snippet children(c)}
      {#if type === 'switch'}
        <Switch id={c.id} {label} hideLabel checked={value === true} disabled={readOnly} aria-describedby={c.describedBy} onchange={(v) => commit(v)} />
      {:else if type === 'select'}
        <Select id={c.id} options={selectOptions} value={String(value ?? '')} disabled={readOnly} aria-describedby={c.describedBy} onchange={(e) => choose(e.currentTarget.value)} />
      {:else}
        <div class="relative">
          {#if type === 'money'}<span class="pointer-events-none absolute inset-y-0 left-3 flex items-center text-body text-muted" aria-hidden="true">$</span>{/if}
          <input
            id={c.id}
            class="input read-only:bg-subtle read-only:text-muted {type === 'money' ? 'pl-7' : ''} {type === 'list' || type === 'text' ? '' : 'tabular-nums'}"
            inputmode={type === 'number' || type === 'money' ? 'decimal' : undefined}
            autocomplete="off"
            spellcheck="false"
            readonly={readOnly}
            {placeholder}
            aria-describedby={c.describedBy}
            aria-invalid={c.invalid || undefined}
            bind:value={draft}
            onfocus={() => (editing = true)}
            onblur={commitText}
            onkeydown={(e) => {
              if (e.key === 'Enter') {
                e.preventDefault();
                commitText();
                editing = true;
              }
            }}
          />
        </div>
      {/if}
    {/snippet}
  </SettingsRow>
{/if}
