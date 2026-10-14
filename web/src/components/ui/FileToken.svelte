<script lang="ts">
  import { bytes } from '../../lib/format';
  import Icon from '../Icon.svelte';
  import IconButton from './IconButton.svelte';
  import Spinner from './Spinner.svelte';

  // An attached file: the one compact object on a tint, because it can be removed (§15.3 row 32).
  let {
    name,
    size,
    state,
    onremove,
    href,
    class: cls = '',
  }: {
    name: string;
    size?: number;
    state?: 'uploading' | 'error';
    onremove?: () => void;
    href?: string;
    class?: string;
  } = $props();
</script>

<span class="inline-flex h-8 max-w-full min-w-0 items-center gap-2 rounded-md bg-subtle px-3 text-small text-text pointer-coarse:h-11 {onremove ? 'pr-0' : ''} {cls}">
  {#if state === 'uploading'}
    <Spinner size={12} class="text-muted" label="Uploading" />
  {:else if state === 'error'}
    <Icon name="alert" size={16} class="text-danger-text" />
  {:else}
    <Icon name="paperclip" size={16} class="text-muted" />
  {/if}
  {#if href}
    <!-- The link fills the token's height so it is a full-size target. -->
    <a
      {href}
      target="_blank"
      rel="noopener noreferrer"
      class="flex min-w-0 items-center self-stretch underline decoration-text/30 underline-offset-2 hover:decoration-text"
      title={name}><span class="truncate" data-copy-ignore>{name}</span></a
    >
  {:else}
    <span class="min-w-0 truncate" title={name} data-copy-ignore>{name}</span>
  {/if}
  {#if state === 'error'}
    <span class="shrink-0 text-danger-text">Didn't upload</span>
  {:else if size !== undefined}
    <span class="shrink-0 text-muted tabular-nums">{bytes(size)}</span>
  {/if}
  {#if onremove}<IconButton icon="close" label="Remove {name}" size="sm" variant="quiet" class="shrink-0" onclick={onremove} />{/if}
</span>
