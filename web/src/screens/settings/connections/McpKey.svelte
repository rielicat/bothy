<script lang="ts">
  import { untrack } from 'svelte';
  import { Select, TextField } from '../../../components/ui';
  import { api } from '../../../lib/api/client';
  import type { SecretMeta } from '../../../lib/api/types.gen';

  // The key a web address server sends: a saved key (`ref`, as the setting
  // names it) or one typed here, in the header named.
  let {
    header = $bindable(''),
    ref = $bindable(''),
    key = $bindable(''),
    optional = true,
    error,
  }: { header?: string; ref?: string; key?: string; optional?: boolean; error?: { header?: string; key?: string } } = $props();

  let saved = $state<SecretMeta[]>([]);
  $effect(() => untrack(() => void api.get<SecretMeta[]>('/api/vault?scope=global').then((l) => (saved = l ?? []), () => {})));
</script>

<TextField label="Header" optional bind:value={header} error={error?.header} placeholder="Authorization" autocomplete="off" spellcheck="false" />
{#if saved.length}
  <Select
    label="Key"
    bind:value={ref}
    options={[{ value: '', label: 'Enter a key' }, ...saved.map((k) => ({ value: k.source === 'aws-sm' ? k.name : `secret:${k.name}`, label: `Saved key: ${k.name}` }))]}
  />
{/if}
{#if !ref}
  <TextField
    label={saved.length ? 'New key' : 'Key'}
    {optional}
    type="password"
    bind:value={key}
    error={error?.key}
    autocomplete="off"
    help="Sent as is, such as Bearer abc123. Stored encrypted. Only sent to this address."
  />
{/if}
