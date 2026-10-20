<script lang="ts">
  import { untrack } from 'svelte';
  import Icon from '../../../components/Icon.svelte';
  import { Button, Dialog, EmptyState, IconButton, Menu, Page, TextField, toast } from '../../../components/ui';
  import { api, ApiError } from '../../../lib/api/client';
  import type { SecretMeta, VaultStatus } from '../../../lib/api/types.gen';
  import { joinNames } from '../../../lib/cardcopy';
  import { config } from '../../../lib/config.svelte';
  import { date, fullTime, relTime } from '../../../lib/format';
  import { session } from '../../../lib/stores/session.svelte';
  import { keyName, storedName, usedBy } from './keyNames';
  import { focusInvalid, relLower, securityBack } from './page.svelte';
  import PageState from './PageState.svelte';
  import UnlockDialog from './UnlockDialog.svelte';

  let { params = {} }: { params?: Record<string, string> } = $props();

  const uid = $props.id();
  // The workspace's keys (Security, admins) or the viewer's own (Account), which
  // take the place of the shared key of the same name when Bothy works for them (SPEC §2.11).
  const personal = $derived(params.group === 'account');
  const scope = $derived(personal ? `user:${session.me?.user.id ?? ''}` : 'global');
  const accountBack = { href: '/settings/account', label: 'Account' };

  let keys = $state<SecretMeta[] | null>(null);
  let failed = $state(false);
  let vault = $state<VaultStatus | null>(null);
  let unlocking = $state(false);

  // One form dialog serves Add and Replace; `target` is the key being replaced.
  let editing = $state(false);
  let target = $state<SecretMeta | null>(null);
  let name = $state('');
  let value = $state('');
  let tried = $state(false);
  let saving = $state(false);
  let formError = $state('');
  let deleting = $state<SecretMeta | null>(null);
  let deleteError = $state('');

  const entries = $derived(config.view?.entries ?? []);
  // Settings point at the shared keys; a personal one is used in their place only by connected tools.
  const users = (k: SecretMeta) => (personal ? '' : joinNames(usedBy(k.name, entries)));
  const sorted = $derived([...(keys ?? [])].sort((a, b) => keyName(a.name).localeCompare(keyName(b.name))));

  async function load() {
    failed = false;
    // What uses a key, and whether the keys are locked, are for admins to see.
    if (session.isAdmin) {
      void config.load();
      api.get<VaultStatus>('/api/vault/status').then((v) => (vault = v), () => {});
    }
    try {
      keys = (await api.get<SecretMeta[]>(`/api/vault?scope=${encodeURIComponent(scope)}`)) ?? [];
    } catch {
      failed = true;
    }
  }

  $effect(() => {
    if (personal || session.isAdmin) untrack(load);
  });

  const path = (n: string) => `/api/vault/${encodeURIComponent(scope)}/${encodeURIComponent(n)}`;

  function openAdd() {
    target = null;
    editing = true;
  }

  function openReplace(k: SecretMeta) {
    target = k;
    editing = true;
  }

  function resetForm() {
    name = value = formError = '';
    tried = false;
  }

  const nameError = $derived.by(() => {
    if (target) return '';
    const n = storedName(name);
    if (!n) return 'Enter a name for the key.';
    if (n.length > 64) return 'Use a shorter name, up to 64 characters.';
    if ((keys ?? []).some((k) => k.name === n)) return 'A key with this name already exists. Replace it instead.';
    return '';
  });
  const valueError = $derived(!value ? 'Paste the key.' : new Blob([value]).size > 65536 ? 'This is too long for a key: keys can be up to 64 KB.' : '');

  // Members don't see the vault's status, so they learn it is locked from a refused change.
  let locked = $state(false);
  function lockedError(e: unknown): string {
    if (!(e instanceof ApiError) || e.code !== 'sealed') return '';
    locked = true;
    return session.isAdmin ? 'Saved keys are locked. Unlock them first.' : 'Saved keys are locked right now. An admin needs to unlock them first.';
  }

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    tried = true;
    formError = '';
    if (nameError || valueError) return focusInvalid(e.currentTarget);
    saving = true;
    const n = target?.name ?? storedName(name);
    try {
      await api.put(path(n), { value });
      editing = false;
      toast(target ? 'Key replaced.' : 'Key saved.');
      void load();
    } catch (e) {
      formError = lockedError(e) || "Couldn't save the key. Try again.";
    } finally {
      saving = false;
    }
  }

  async function remove() {
    if (!deleting) return;
    saving = true;
    deleteError = '';
    try {
      await api.del(path(deleting.name));
      keys = (keys ?? []).filter((k) => k.name !== deleting!.name);
      deleting = null;
      toast('Key deleted.');
    } catch (e) {
      deleteError = lockedError(e) || "Couldn't delete the key. Try again.";
    } finally {
      saving = false;
    }
  }

  const helpFor = (k: SecretMeta | null) => {
    const who = k ? users(k) : '';
    return `Stored encrypted. Only sent to ${who || 'the service that uses it'}.`;
  };
</script>

<Page title={personal ? 'Your saved keys' : 'Saved keys'} back={personal ? accountBack : securityBack}>
  {#snippet actions()}
    {#if (personal || session.isAdmin) && keys && !vault?.sealed && !locked}<Button onclick={openAdd}>Add key</Button>{/if}
  {/snippet}
  {#if !personal && !session.isAdmin}
    <PageState kind="forbidden" />
  {:else if failed}
    <PageState kind="failed" onretry={load} />
  {:else if keys === null}
    <PageState kind="loading" />
  {:else}
    {#if personal}
      <p class="mb-4 text-body text-muted">Keys only you use. When a connected tool works for you, it uses your key in place of the shared key with the same name.</p>
    {/if}
    <!-- While the locked-keys banner shows, its Unlock is the one action; a second row here would repeat it. -->
    {#if vault?.sealed && !(session.isAdmin && session.banners.some((b) => b.code === 'vault_sealed' && !session.dismissed.includes(b.code)))}
      <div class="flex min-h-14 flex-wrap items-center justify-between gap-x-6 gap-y-2 border-b border-border py-3">
        <!-- Locked by a key file that doesn't match, no passphrase can unlock them. -->
        {#if vault.kek_source === 'passphrase'}
          <p class="text-body text-text">Saved keys are locked.</p>
          <Button variant="primary" size="sm" onclick={() => (unlocking = true)}>Unlock</Button>
        {:else}
          <p class="text-body text-text">Saved keys are locked because Bothy's key file is missing or doesn't match. Put it back on the server, then restart Bothy.</p>
        {/if}
      </div>
    {/if}
    {#if !sorted.length}
      <EmptyState text="No saved keys yet." more={personal ? 'Add one when a connected tool should use your own account.' : 'Keys you add for models and connections show up here.'} />
    {:else}
      {#each sorted as k (k.source + ':' + k.name)}
        {@const who = users(k)}
        {@const label = keyName(k.name)}
        <div class="flex min-h-14 items-center gap-3 border-b border-border py-2 last:border-b-0 pointer-coarse:min-h-12">
          <div class="min-w-0 flex-1">
            <p class="text-body text-text [overflow-wrap:anywhere]">{label}</p>
            <!-- Each piece stays whole and the "·" stays with the piece before it, so phones wrap between pieces. -->
            <p class="text-small text-muted">
              {#if who}Used by {who}{'\u00a0· '}{/if}<span class="whitespace-nowrap"
                >{#if k.source === 'env'}{who ? 'set' : 'Set'} on the server{:else if date(k.last_used_at)}{who ? 'last' : 'Last'} used <time
                    datetime={k.last_used_at}
                    title={fullTime(k.last_used_at)}>{relLower(k.last_used_at)}</time
                  >{:else}{who ? 'not' : 'Not'} used yet{/if}</span
              >
            </p>
          </div>
          {#if k.source === 'vault' && !vault?.sealed && !locked}
            <Button size="sm" aria-label="Replace {label}" onclick={() => openReplace(k)}>Replace</Button>
            <Menu items={[{ label: 'Delete…', danger: true, onselect: () => ((deleting = k), (deleteError = '')) }]} placement="bottom-end">
              {#snippet trigger(props)}<IconButton {...props} icon="more" label="More for {label}" size="sm" />{/snippet}
            </Menu>
          {/if}
        </div>
      {/each}
    {/if}
  {/if}
</Page>

<Dialog bind:open={editing} title={target ? `Replace ${keyName(target.name)}` : 'Add key'} size="md" busy={saving} dirty={!!(name || value)} onclose={resetForm}>
  <form id="{uid}-key" class="space-y-5" onsubmit={submit} novalidate>
    {#if !target}
      <TextField
        label="Name"
        placeholder="Brave Search API key"
        autocomplete="off"
        bind:value={name}
        help={personal ? 'Use the name of the shared key the connected tool uses. An admin can tell you what it is.' : undefined}
        error={tried ? nameError : ''}
      />
    {/if}
    <TextField label={target ? 'New value' : 'Value'} type="password" autocomplete="off" bind:value help={helpFor(target)} error={tried ? valueError : ''} />
    {#if formError}<p class="field-error" role="alert"><Icon name="alert" class="mt-px" />{formError}</p>{/if}
  </form>
  {#snippet footer({ close })}
    <Button data-cancel onclick={close}>Cancel</Button>
    <Button variant="primary" type="submit" form="{uid}-key" loading={saving} loadingLabel="Saving…">Save key</Button>
  {/snippet}
</Dialog>

<Dialog open={!!deleting} title={deleting ? `Delete ${keyName(deleting.name)}?` : ''} initialFocus="cancel" busy={saving} onclose={() => (deleting = null)}>
  {#if deleting}
    {@const who = users(deleting)}
    <p class="text-body text-text">{who ? `${who} will stop working until you add a new key. ` : ''}This can't be undone.</p>
  {/if}
  {#if deleteError}<p class="field-error" role="alert"><Icon name="alert" class="mt-px" />{deleteError}</p>{/if}
  {#snippet footer({ close })}
    <Button data-cancel onclick={close}>Cancel</Button>
    <Button variant="danger" loading={saving} loadingLabel="Deleting…" onclick={remove}>Delete key</Button>
  {/snippet}
</Dialog>

<UnlockDialog bind:open={unlocking} first={!!vault?.new_passphrase} onunlocked={load} />
