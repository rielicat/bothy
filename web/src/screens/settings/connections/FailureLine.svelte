<script lang="ts">
  import { StatusLine } from '../../../components/ui';
  import type { Failure } from './connections';

  // What a check or save ran into, in plain words with the raw error behind Details; nothing while it worked.
  let { failure, action, class: cls = '' }: { failure?: Failure | null; action?: { label: string; onclick: () => void }; class?: string } = $props();
</script>

{#if failure}<StatusLine class={cls} tone="danger" alert text={failure.message} details={failure.details} {action} />{/if}
