<script lang="ts">
  import IconButton from '../../components/ui/IconButton.svelte';
  import TextField from '../../components/ui/TextField.svelte';

  // A password field with an eye toggle inside its end (§5.5, §5.9).
  let {
    label,
    value = $bindable(''),
    input = $bindable(),
    error,
    help,
    autocomplete,
    oninput,
    class: cls = '',
  }: {
    label: string;
    value?: string;
    input?: HTMLInputElement;
    error?: string;
    help?: string;
    autocomplete: 'new-password' | 'current-password';
    oninput?: () => void;
    class?: string;
  } = $props();

  let show = $state(false);
</script>

<TextField {label} type={show ? 'text' : 'password'} bind:value bind:input {error} {help} {autocomplete} {oninput} class={cls}>
  {#snippet trailing()}
    <IconButton icon={show ? 'eye-off' : 'eye'} label={show ? 'Hide password' : 'Show password'} size="sm" variant="quiet" aria-controls={input?.id} onclick={() => (show = !show)} />
  {/snippet}
</TextField>
