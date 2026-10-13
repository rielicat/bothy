<script lang="ts">
  import Icon from '../../components/Icon.svelte';
  import Section from '../../components/Section.svelte';
  import { api, errorText, qs } from '../../lib/api/client';
  import type { SecretMeta, VaultStatus } from '../../lib/api/types.gen';
  import { ago, isZeroTime } from '../../lib/format';
  import { session } from '../../lib/stores/session.svelte';
  import { attempt, toasts } from '../../lib/stores/ui.svelte';

  let status = $state<VaultStatus | null>(null);
  let secrets = $state<SecretMeta[]>([]);
  let scope = $state(session.isAdmin ? 'global' : `user:${session.me?.user.id}`);
  let replacing = $state('');
  let value = $state('');
  let add = $state({ name: '', value: '' });
  let passphrase = $state('');

  const kek: Record<string, string> = {
    docker_secret: 'Docker secret',
    systemd: 'systemd credential',
    file: 'key file',
    passphrase: 'passphrase (entered after each restart)',
    env: 'environment variable',
    autogen: 'generated key file',
  };

  async function load() {
    secrets = await api.get<SecretMeta[]>(`/api/vault${qs({ scope })}`).catch(() => []);
  }

  $effect(() => {
    void scope;
    void load();
  });

  $effect(() => {
    if (session.isAdmin) api.get<VaultStatus>('/api/vault/status').then((s) => (status = s), () => {});
  });

  const path = (name: string) => `/api/vault/${encodeURIComponent(scope)}/${encodeURIComponent(name)}`;

  async function put(name: string, v: string) {
    try {
      await api.put(path(name), { value: v });
      toasts.ok(`${name} saved. The value is encrypted and never shown again.`);
      replacing = value = '';
      add = { name: '', value: '' };
      void load();
    } catch (e) {
      toasts.error(e);
    }
  }

  async function unseal(e: SubmitEvent) {
    e.preventDefault();
    try {
      status = await api.post<VaultStatus>('/api/vault/unseal', { passphrase });
      passphrase = '';
    } catch (err) {
      toasts.push(errorText(err), 'error');
    }
  }
</script>

{#if status}
  <Section title="Encryption key">
    <div class="space-y-3 py-4 text-sm">
      <p>
        Secrets are sealed with a key from the <strong>{kek[status.kek_source] ?? status.kek_source}</strong>
        <code class="text-xs text-muted">· {status.kek_id}</code> · {status.secrets} secret{status.secrets === 1 ? '' : 's'}
      </p>
      {#if status.colocated}
        <p class="alert-warn"><Icon name="alert" size={16} />The key sits on the same volume as the database, so a copy of /data includes both. Mount a separate /keys volume.</p>
      {/if}
      {#if status.kek_source === 'env'}
        <p class="alert-warn"><Icon name="alert" size={16} />The key comes from an environment variable, which other processes can often read.</p>
      {/if}
      {#if status.sealed}
        <form class="flex flex-wrap gap-2" onsubmit={unseal}>
          <input class="input max-w-xs" type="password" autocomplete="off" placeholder="Vault passphrase" aria-label="Vault passphrase" required bind:value={passphrase} />
          <button class="btn btn-primary"><Icon name="lock" size={15} />Unseal</button>
        </form>
      {/if}
    </div>
  </Section>
{/if}

<Section title="Secrets" desc="Write-only: values go in, never come back out. Only the gateway uses them, never a sandbox.">
  {#snippet actions()}
    {#if session.isAdmin}
      <select class="input h-8 w-auto text-sm" aria-label="Scope" bind:value={scope}>
        <option value="global">Shared</option>
        <option value="user:{session.me?.user.id}">Only mine</option>
      </select>
    {/if}
  {/snippet}
  {#each secrets as s (s.source + ':' + s.scope + ':' + s.name)}
    <div class="py-3">
      <div class="flex flex-wrap items-center gap-3 text-sm">
        <Icon name="key" size={16} class="text-muted" />
        <span class="mono min-w-0 flex-1 truncate">{s.name}</span>
        <span class="mono text-xs text-muted">…{s.hint}</span>
        <span class="chip {s.source === 'env' ? 'chip-info' : ''}" title={s.source === 'env' ? 'From BOTHY_SECRET_* in the environment; it shadows the vault' : ''}>{s.source}</span>
        <span class="text-xs text-faint">used {isZeroTime(s.last_used_at) ? 'never' : ago(s.last_used_at)}</span>
        {#if s.source === 'vault'}
          <button type="button" class="btn btn-sm" onclick={() => ((replacing = replacing === s.name ? '' : s.name), (value = ''))}>Replace</button>
          <button type="button" class="btn btn-ghost btn-icon btn-sm" aria-label="Delete {s.name}" onclick={async () => (await attempt(() => api.del(path(s.name)), `${s.name} deleted.`)) && load()}><Icon name="trash" size={14} /></button>
        {/if}
      </div>
      {#if replacing === s.name}
        <form class="mt-2 flex gap-2" onsubmit={(e) => (e.preventDefault(), put(s.name, value))}>
          <input class="input mono" type="password" autocomplete="off" aria-label="New value for {s.name}" required bind:value />
          <button class="btn btn-primary">Save</button>
        </form>
      {/if}
    </div>
  {:else}
    <p class="py-4 text-sm text-muted">No secrets in this scope.</p>
  {/each}
  <form class="grid gap-2 py-4 sm:grid-cols-[1fr_1fr_auto]" onsubmit={(e) => (e.preventDefault(), put(add.name.trim(), add.value))}>
    <input class="input mono" placeholder="name, e.g. brave_api_key" aria-label="Secret name" required pattern="[A-Za-z0-9_.\-]+" bind:value={add.name} />
    <input class="input mono" type="password" autocomplete="off" placeholder="value" aria-label="Secret value" required bind:value={add.value} />
    <button class="btn">Add secret</button>
  </form>
  <p class="help pb-4">Reference it in settings as <code>secret:name</code>.</p>
</Section>
