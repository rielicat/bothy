<script lang="ts">
  import Icon from '../../components/Icon.svelte';
  import Section from '../../components/Section.svelte';
  import { api, errorText } from '../../lib/api/client';
  import type { TokenCreateResp, TokenRecord } from '../../lib/api/types.gen';
  import { ago, isZeroTime, when } from '../../lib/format';
  import { session } from '../../lib/stores/session.svelte';
  import { attempt, copyText } from '../../lib/stores/ui.svelte';

  let tokens = $state<TokenRecord[]>([]);
  let form = $state({ name: '', scopes: ['chat'] as string[], days: 90, on_approval: 'deny', rate: 30 });
  let created = $state<TokenCreateResp | null>(null);
  let error = $state('');

  const scopes = $derived([
    ['chat', 'Chat with Bothy (sessions API and the bothy models on /v1)'],
    ['read', 'Read-only access to the API and live events'],
    ['llm', 'Use your providers directly through /v1 (passthrough)'],
    ...(session.isAdmin ? [['admin', 'Administer Bothy (settings, users)']] : []),
  ]);
  const origin = location.origin;
  const active = $derived(tokens.filter((t) => isZeroTime(t.revoked_at)));

  $effect(() => {
    api.get<TokenRecord[]>('/api/tokens').then((t) => (tokens = t), (e) => (error = errorText(e)));
  });

  async function create(e: SubmitEvent) {
    e.preventDefault();
    error = '';
    try {
      created = await api.post<TokenCreateResp>('/api/tokens', { name: form.name, scopes: form.scopes, ttl: form.days * 86400e9, on_approval: form.on_approval, rate_per_min: form.rate });
      if (created.meta) tokens = [...tokens, created.meta];
      form.name = '';
    } catch (err) {
      error = errorText(err);
    }
  }

  async function revoke(t: TokenRecord) {
    if (await attempt(() => api.del(`/api/tokens/${encodeURIComponent(t.id)}`), `${t.name} revoked.`)) tokens = tokens.filter((x) => x.id !== t.id);
  }

  const curl = (tok: string) =>
    `curl -s ${origin}/v1/chat/completions \\\n  -H "Authorization: Bearer ${tok}" \\\n  -H "Content-Type: application/json" \\\n  -d '{"model":"bothy","messages":[{"role":"user","content":"Hello"}]}'`;
</script>

{#if created}
  <div class="alert-ok mb-6 flex-col">
    <p class="font-medium">Copy your new token now. It will not be shown again.</p>
    <div class="flex w-full gap-2">
      <code class="mono min-w-0 flex-1 truncate rounded-lg bg-surface px-3 py-2 text-fg">{created.token}</code>
      <button type="button" class="btn" onclick={() => copyText(created!.token, 'Token copied')}><Icon name="copy" size={15} />Copy</button>
    </div>
    <details class="w-full text-fg">
      <summary class="cursor-pointer text-sm">How to use it</summary>
      <p class="mt-2 text-sm">curl:</p>
      <pre class="mono mt-1 overflow-x-auto rounded-lg bg-surface p-3">{curl(created.token)}</pre>
      <p class="mt-3 text-sm">Open WebUI: Settings → Connections → OpenAI API, URL <code>{origin}/v1</code>, key = the token, model <code>bothy</code>.</p>
    </details>
    <button type="button" class="btn btn-sm self-end" onclick={() => (created = null)}>Done</button>
  </div>
{/if}

<Section title="Your tokens" desc="For scripts and other apps. Tokens can never approve actions.">
  {#each active as t (t.id)}
    <div class="flex flex-wrap items-center gap-3 py-3.5 text-sm">
      <Icon name="code" size={16} class="text-muted" />
      <div class="min-w-0 flex-1">
        <p class="font-medium">{t.name}</p>
        <p class="text-xs text-muted">{t.scopes.join(', ')} · on approval: {t.on_approval} · {t.rate_per_min}/min · expires {when(t.expires_at)}</p>
      </div>
      <span class="text-xs text-faint">used {isZeroTime(t.last_used_at) ? 'never' : ago(t.last_used_at)}</span>
      <button type="button" class="btn btn-ghost btn-sm text-bad" onclick={() => revoke(t)}>Revoke</button>
    </div>
  {:else}
    <p class="py-4 text-sm text-muted">No tokens yet.</p>
  {/each}
</Section>

<Section title="Create a token">
  <form class="grid gap-4 py-4" onsubmit={create}>
    <div class="grid gap-3 sm:grid-cols-2">
      <div><label class="label" for="tk-name">Name</label><input id="tk-name" class="input" required placeholder="Open WebUI on the laptop" bind:value={form.name} /></div>
      <div>
        <label class="label" for="tk-days">Expires after</label>
        <select id="tk-days" class="input" bind:value={form.days}>
          {#each [7, 30, 90, 365] as d (d)}<option value={d}>{d} days</option>{/each}
        </select>
      </div>
    </div>
    <fieldset class="space-y-2">
      <legend class="label">Allowed to</legend>
      {#each scopes as [v, l] (v)}
        <label class="flex items-start gap-2 text-sm"><input type="checkbox" class="mt-0.5 size-4 accent-accent" value={v} bind:group={form.scopes} /><span><code>{v}</code> · {l}</span></label>
      {/each}
    </fieldset>
    <div class="grid gap-3 sm:grid-cols-2">
      <div>
        <label class="label" for="tk-appr">When an action needs approval</label>
        <select id="tk-appr" class="input" bind:value={form.on_approval}>
          <option value="deny">Deny it</option>
          <option value="wait">Wait for someone to approve (up to the API wait)</option>
        </select>
      </div>
      <div><label class="label" for="tk-rate">Requests per minute</label><input id="tk-rate" class="input" type="number" min="1" max="600" bind:value={form.rate} /></div>
    </div>
    {#if error}<p class="alert-bad" role="alert">{error}</p>{/if}
    <button class="btn btn-primary justify-self-start" disabled={!form.scopes.length}>Create token</button>
  </form>
</Section>
