<script lang="ts">
  import Icon from '../Icon.svelte';
  import CodeBlock from './CodeBlock.svelte';
  import Disclosure from './Disclosure.svelte';
  import LinkButton from './LinkButton.svelte';
  import Spinner from './Spinner.svelte';

  let {
    tone,
    text,
    details,
    action,
    alert = false,
    id,
    class: cls = '',
  }: {
    tone: 'progress' | 'success' | 'warning' | 'danger';
    text: string;
    /** The raw message, verbatim, behind "Details" (§14.4). */
    details?: string;
    action?: { label: string; onclick: () => void };
    /** For danger lines that follow a person's action: announced once as an alert. */
    alert?: boolean;
    id?: string;
    class?: string;
  } = $props();

  const role = $derived(tone === 'progress' || tone === 'success' ? 'status' : tone === 'danger' && alert ? 'alert' : undefined);
  const color = { progress: 'text-text', success: 'text-text', warning: 'text-warning-text', danger: 'text-danger-text' };
</script>

<div class="text-body {cls}">
  <div class="flex items-start gap-2">
    <span class="flex h-5 w-4 shrink-0 items-center justify-center">
      {#if tone === 'progress'}
        <Spinner size={16} class="text-muted" />
      {:else if tone === 'success'}
        <Icon name="check" class="text-accent-text" />
      {:else if tone === 'warning'}
        <Icon name="alert" class="text-warning-text" />
      {:else}
        <Icon name="alert" class="text-danger-text" />
      {/if}
    </span>
    <p {id} {role} class="min-w-0 flex-1 {color[tone]}">
      {text}{#if action}{' '}<LinkButton class="ml-1" onclick={action.onclick}>{action.label}</LinkButton>{/if}
    </p>
  </div>
  {#if details}
    <Disclosure class="mt-1 pl-6">
      <CodeBlock code={details} wrap />
    </Disclosure>
  {/if}
</div>
