<script lang="ts">
  import { CodeBlock, Disclosure, TextField } from '../../../components/ui';
  import { unlock } from './vault';

  // The passphrase fields of the unlock screen and dialog, and what went wrong; the form around them calls submit.
  // first: no passphrase chosen yet (VaultStatus.new_passphrase), which this sets, so it is typed twice.
  let {
    first,
    passphrase = $bindable(''),
    busy = $bindable(false),
    class: cls = '',
    onunlocked,
  }: { first: boolean; passphrase?: string; busy?: boolean; class?: string; onunlocked: () => void } = $props();

  let again = $state('');
  let error = $state('');
  let details = $state('');
  let input: HTMLInputElement | undefined = $state();

  export async function submit(e: SubmitEvent) {
    e.preventDefault();
    busy = true;
    const r = await unlock(passphrase, first ? again : undefined);
    busy = false;
    if (r.ok) return onunlocked();
    error = r.error;
    details = r.details ?? '';
    input?.focus();
  }
</script>

<TextField class={cls} label="Passphrase" type="password" autocomplete={first ? 'new-password' : 'off'} bind:value={passphrase} bind:input error={first ? undefined : error} />
{#if first}<TextField class="mt-4" label="Type it again" type="password" autocomplete="new-password" bind:value={again} {error} />{/if}
{#if details}
  <Disclosure class="mt-2"><CodeBlock code={details} wrap /></Disclosure>
{/if}
