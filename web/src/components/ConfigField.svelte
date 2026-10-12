<script lang="ts">
  import { config } from '../lib/config.svelte';
  import { parseGoDuration, prettyGoDuration } from '../lib/format';
  import { router } from '../lib/router.svelte';
  import SourceChip from './SourceChip.svelte';

  type Kind = 'text' | 'number' | 'bool' | 'select' | 'duration' | 'list' | 'usd' | 'secret' | 'textarea';

  let {
    path,
    label,
    help = '',
    type,
    options,
    placeholder = '',
    step,
  }: { path: string; label: string; help?: string; type?: Kind; options?: [string, string][]; placeholder?: string; step?: number } = $props();

  const id = $props.id();
  const e = $derived(config.entry(path));
  const sch = $derived(config.schema(path));
  const kind: Kind = $derived.by(() => {
    if (type) return type;
    if (e?.secret) return 'secret';
    const t = Array.isArray(sch?.type) ? sch.type[0] : sch?.type;
    if (sch?.enum) return 'select';
    if (t === 'boolean') return 'bool';
    if (t === 'integer' || t === 'number') return 'number';
    if (sch?.format === 'duration') return 'duration';
    if (t === 'array') return 'list';
    return 'text';
  });
  const opts: [string, string][] = $derived(options ?? (sch?.enum ?? []).map((v) => [String(v), String(v)]));
  const value = $derived(e?.value);
  const locked = $derived(!!e?.locked);
  const err = $derived(config.errors[path]);

  function text(v: unknown): string {
    if (v === null || v === undefined) return '';
    if (kind === 'list') return Array.isArray(v) ? v.join('\n') : '';
    if (kind === 'duration') return prettyGoDuration(String(v));
    return String(v);
  }

  let draft = $state('');
  $effect(() => {
    draft = text(value);
  });

  // bind:value on a number input yields a number, so normalise first.
  function parse(s: string | number | null): { ok: true; v: unknown } | { ok: false; msg: string } {
    const t = String(s ?? '').trim();
    switch (kind) {
      case 'number':
      case 'usd': {
        const n = Number(t);
        if (t === '' || !Number.isFinite(n)) return { ok: false, msg: 'Enter a number.' };
        return { ok: true, v: sch?.type === 'integer' ? Math.round(n) : n };
      }
      case 'duration':
        return parseGoDuration(t) === null ? { ok: false, msg: 'Use a duration such as 45s, 15m or 2h30m.' } : { ok: true, v: t };
      case 'list':
        return { ok: true, v: t ? t.split(/[\n,]/).map((x) => x.trim()).filter(Boolean) : [] };
      case 'secret':
        return !t || /secret:[A-Za-z0-9_.-]+/.test(t) ? { ok: true, v: t } : { ok: false, msg: 'Use a vault reference like secret:my_key; store the value under Vault.' };
      default:
        return { ok: true, v: String(s ?? '') };
    }
  }

  async function commit(v: unknown) {
    if (JSON.stringify(v) === JSON.stringify(value)) return;
    await config.set({ [path]: v });
  }

  function onblur() {
    const r = parse(draft);
    if (!r.ok) config.errors[path] = r.msg;
    else void commit(r.v);
  }
</script>

<div class="grid gap-x-6 gap-y-1.5 py-3.5 sm:grid-cols-[minmax(0,2fr)_minmax(0,3fr)]">
  <div>
    <label for="{id}-in" class="text-sm font-medium">{label}</label>
    <div class="mt-1 flex flex-wrap items-center gap-1.5"><SourceChip entry={e} /></div>
    {#if help}<p class="help">{help}</p>{/if}
  </div>
  <div class="min-w-0">
    {#if kind === 'bool'}
      <input id="{id}-in" type="checkbox" class="switch mt-1" checked={value === true} disabled={locked} onchange={(ev) => commit(ev.currentTarget.checked)} />
    {:else if kind === 'select'}
      <select id="{id}-in" class="input" value={String(value ?? '')} disabled={locked} onchange={(ev) => commit(ev.currentTarget.value === '' && opts.some(([v]) => v === '') ? null : ev.currentTarget.value)}>
        {#each opts as [v, l] (v)}<option value={v}>{l}</option>{/each}
      </select>
    {:else if kind === 'list' || kind === 'textarea'}
      <textarea id="{id}-in" class="input mono min-h-20" rows="3" {placeholder} disabled={locked} bind:value={draft} {onblur}></textarea>
      {#if kind === 'list'}<p class="help">One per line.</p>{/if}
    {:else}
      <div class="relative">
        {#if kind === 'usd'}<span class="pointer-events-none absolute top-2 left-3 text-sm text-muted">$</span>{/if}
        <input
          id="{id}-in"
          class="input {kind === 'usd' ? 'pl-6' : ''} {kind === 'duration' || kind === 'secret' ? 'mono' : ''}"
          type={kind === 'number' || kind === 'usd' ? 'number' : 'text'}
          step={step ?? (kind === 'usd' ? 0.01 : sch?.type === 'integer' ? 1 : 'any')}
          min={sch?.minimum}
          {placeholder}
          disabled={locked}
          aria-invalid={!!err}
          bind:value={draft}
          {onblur}
          onkeydown={(ev) => ev.key === 'Enter' && ev.currentTarget.blur()}
        />
      </div>
      {#if kind === 'secret'}<p class="help">A vault reference. Values live in <a class="link" href="/settings/vault" onclick={router.link}>Vault</a>.</p>{/if}
    {/if}
    {#if err}<p class="mt-1 text-xs text-bad" role="alert">{err}</p>{/if}
  </div>
</div>
