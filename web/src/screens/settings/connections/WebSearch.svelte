<script lang="ts">
  import { untrack } from 'svelte';
  import { Button, KeyField, OptionList, StatusLine, TextField, toast, type CheckResult } from '../../../components/ui';
  import Loading from '../../../components/ui/Loading.svelte';
  import { api, errorText } from '../../../lib/api/client';
  import type { SecretMeta } from '../../../lib/api/types.gen';
  import { config } from '../../../lib/config.svelte';
  import { hostPort } from '../../../lib/providers';
  import ConnPage from './ConnPage.svelte';
  import { failureCode, saveConfig, searchFailure, secretName, type SearchProvider } from './connections';
  import type { SearchCheck } from './types';

  type Line = { tone: 'progress' | 'success' | 'danger'; text: string; details?: string; allow?: string };

  let ready = $state(false);
  let provider = $state('none');
  let address = $state('');
  let keys = $state<Record<string, string>>({ brave: '', tavily: '' });
  let secrets = $state<SecretMeta[]>([]);
  let line = $state<Line | null>(null);
  let timer: ReturnType<typeof setTimeout> | undefined;
  let run = 0;

  const consoles: Record<string, string> = { brave: 'https://api-dashboard.search.brave.com/app/keys', tavily: 'https://app.tavily.com/home' };
  const keyPath = (p: string) => `search.${p}_api_key`;
  const keyName = (p: string) => secretName(keyPath(p), `${p}_api_key`);
  const saved = (p: string) => secrets.some((s) => s.name === keyName(p));

  $effect(() => {
    untrack(() =>
      Promise.all([config.load(true), api.get<SecretMeta[]>('/api/vault').catch(() => [])]).then(([, s]) => {
        secrets = s;
        provider = config.value<string>('search.provider', 'none');
        address = config.value<string>('search.searxng_url', '') || 'http://searxng:8080';
        ready = true;
      }),
    );
    return () => clearTimeout(timer);
  });

  /** Checks a search provider with one search (POST /api/search/test), then saves it. */
  async function check(p: SearchProvider, key = ''): Promise<CheckResult & { allow?: string }> {
    const body = { provider: p, searxng_url: p === 'searxng' ? address.trim() : undefined, api_key: key || undefined };
    let r: SearchCheck;
    try {
      r = await api.post<SearchCheck>('/api/search/test', body);
    } catch (e) {
      r = { ok: false, code: failureCode(e) as SearchCheck['code'], error: errorText(e) };
    }
    if (!r.ok) {
      const f = searchFailure(p, r.code ?? 'unknown', r.error ?? '');
      return {
        ok: false,
        message: f.message,
        details: f.details,
        allow: f.allow ? hostPort(address.trim()) : undefined,
        action: r.code === 'key_rejected' && consoles[p] ? { label: 'Get a new key ↗', onclick: () => window.open(consoles[p], '_blank', 'noopener,noreferrer') } : undefined,
      };
    }
    const set: Record<string, unknown> = { 'search.provider': p };
    if (p === 'searxng') set['search.searxng_url'] = address.trim();
    else if (key) {
      try {
        await api.put(`/api/vault/global/${keyName(p)}`, { value: key });
      } catch (e) {
        return { ok: false, message: "Bothy couldn't save the key. Try again.", details: errorText(e) };
      }
      set[keyPath(p)] = `secret:${keyName(p)}`;
    }
    const bad = await saveConfig(set);
    return bad ? { ok: false, message: bad.message, details: bad.details } : { ok: true, message: 'Search works.' };
  }

  async function checkAddress() {
    clearTimeout(timer);
    if (!/^https?:\/\/\S+$/.test(address.trim())) return;
    const mine = ++run;
    line = { tone: 'progress', text: 'Checking…' };
    const r = await check('searxng');
    if (mine !== run) return;
    line = r.ok ? { tone: 'success', text: r.message } : { tone: 'danger', text: r.message, details: r.details, allow: r.allow };
  }

  async function allow() {
    line = { tone: 'progress', text: 'Checking…' };
    const bad = await saveConfig({ 'search.searxng_allow_private': true });
    if (bad) line = { tone: 'danger', text: bad.message, details: bad.details };
    else await checkAddress();
  }

  async function choose(p: string) {
    clearTimeout(timer);
    run++;
    line = null;
    if (p === 'none') {
      const bad = await saveConfig({ 'search.provider': 'none' });
      if (bad) line = { tone: 'danger', text: bad.message, details: bad.details };
      else toast('Web search turned off.');
    } else if (p === 'searxng') void checkAddress();
    else if (saved(p)) {
      line = { tone: 'progress', text: 'Checking the key…' };
      const r = await check(p as SearchProvider);
      line = r.ok ? { tone: 'success', text: r.message } : { tone: 'danger', text: r.message, details: r.details };
    }
  }

  const checkKey = (p: SearchProvider) => async (v: string, signal: AbortSignal) => {
    const r = await check(p, v);
    if (!signal.aborted && r.ok) {
      line = { tone: 'success', text: r.message };
      secrets = await api.get<SecretMeta[]>('/api/vault').catch(() => secrets);
    }
    return r;
  };
</script>

<ConnPage title="Web search">
  {#if !ready}
    <Loading />
  {:else}
    <OptionList
      name="search"
      label="Web search"
      bind:value={provider}
      onchange={choose}
      options={[
        { value: 'none', title: 'Off', description: "Bothy doesn't search the web.", expandable: false },
        { value: 'searxng', title: 'SearXNG', description: 'Private search you host yourself' },
        { value: 'brave', title: 'Brave Search', description: 'Needs an API key from Brave' },
        { value: 'tavily', title: 'Tavily', description: 'Needs an API key from Tavily' },
      ]}
    >
      {#snippet expanded(v)}
        {#if v === 'searxng'}
          <TextField
            label="Address"
            type="url"
            bind:value={address}
            spellcheck="false"
            placeholder="http://searxng:8080"
            oninput={() => {
              clearTimeout(timer);
              line = null;
              timer = setTimeout(checkAddress, 800);
            }}
            onkeydown={(e: KeyboardEvent) => e.key === 'Enter' && checkAddress()}
          />
        {:else if v === 'brave' || v === 'tavily'}
          {#key v}
            <KeyField
              label="API key"
              bind:value={keys[v]}
              saved={saved(v)}
              minLength={16}
              checkingLabel="Checking the key…"
              check={checkKey(v)}
              help="Stored encrypted. Only sent to {v === 'brave' ? 'Brave' : 'Tavily'}."
            />
          {/key}
        {/if}
        {#if line && (v === 'searxng' || saved(v))}
          {#if line.allow}
            <!-- A private address is a question for the admin, not an error. -->
            <p class="mt-3 text-body text-text" role="status">{line.text}</p>
            <Button class="mt-3" size="sm" onclick={allow}>Allow {line.allow}</Button>
          {:else}
            <StatusLine class="mt-3" tone={line.tone} text={line.text} details={line.details} alert />
          {/if}
        {/if}
      {/snippet}
    </OptionList>
  {/if}
</ConnPage>
