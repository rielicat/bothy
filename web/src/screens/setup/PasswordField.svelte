<script lang="ts">
  import LinkButton from '../../components/ui/LinkButton.svelte';
  import TextField from '../../components/ui/TextField.svelte';

  // A password field with "Show" / "Hide" at the end of its label row (§5.5, §5.9).
  let {
    label,
    value = $bindable(''),
    input = $bindable(),
    error,
    help,
    autocomplete,
    oninput,
  }: {
    label: string;
    value?: string;
    input?: HTMLInputElement;
    error?: string;
    help?: string;
    autocomplete: 'new-password' | 'current-password';
    oninput?: () => void;
  } = $props();

  let show = $state(false);
</script>

<TextField {label} type={show ? 'text' : 'password'} bind:value bind:input {error} {help} {autocomplete} {oninput}>
  {#snippet labelAction()}
    <LinkButton onclick={() => (show = !show)} aria-pressed={show} aria-controls={input?.id}>{show ? 'Hide' : 'Show'}</LinkButton>
  {/snippet}
</TextField>
