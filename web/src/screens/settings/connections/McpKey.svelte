<script lang="ts">
  import { untrack } from 'svelte';
  import { Select, TextField } from '../../../components/ui';
  import { api } from '../../../lib/api/client';
  import type { SecretMeta } from '../../../lib/api/types.gen';
  import { keyName } from '../security/keyNames';

  // The key a web address server sends: a saved key (`ref`, as the setting
  // names it) or one typed here, in the header named.
  let {
    header = $bindable(''),
    ref = $bindable(''),
    key = $bindable(''),
    optional = true,
    askHeader = true,
    help = 'Sent as is, such as Bearer abc123. Stored encrypted. Only sent to this address.',
    error,
  }: {
    header?: string;
    ref?: string;
    key?: string;
    optional?: boolean;
    askHeader?: boolean;
    help?: string;
    error?: { header?: string; key?: string };
  } = $props();

  let saved = $state<SecretMeta[]>([]);
  // Bothy's own keys (chat app tokens, sign-in and GitHub secrets) are never sent to a tool; the server refuses them too.
  const options = $derived(
    saved
      .filter((k) => !k.system)
      .map((k) => ({ value: k.source === 'aws-sm' ? k.name : `secret:${k.name}`, label: keyName(k.name) }))
      .sort((a, b) => a.label.localeCompare(b.label)),
  );
  $effect(() => untrack(() => void api.get<SecretMeta[]>('/api/vault?scope=global').then((l) => (saved = l ?? []), () => {})));
</script>

{#if askHeader}
  <TextField label="Header" optional bind:value={header} error={error?.header} placeholder="Authorization" autocomplete="off" spellcheck="false" />
{/if}
{#if options.length}
  <Select
    label="Key"
    bind:value={ref}
    options={[{ value: '', label: 'Enter a key' }, ...options]}
  />
{/if}
{#if !ref}
  <TextField
    label={options.length ? 'New key' : 'Key'}
    {optional}
    type="password"
    bind:value={key}
    error={error?.key}
    autocomplete="off"
    {help}
  />
{/if}
