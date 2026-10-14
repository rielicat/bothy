<script lang="ts">
  import { onMount } from 'svelte';
  import Icon from '../../components/Icon.svelte';
  import Button from '../../components/ui/Button.svelte';
  import Dialog from '../../components/ui/Dialog.svelte';
  import DiffView from '../../components/ui/DiffView.svelte';
  import Notice from '../../components/ui/Notice.svelte';
  import { proseDiff } from './text';

  // Editing notes or memory in place (§9.2, §9.4): a sans textarea that grows
  // with its Markdown source, ⌘S / Ctrl S to save, Esc to cancel, and the
  // edit-conflict notice with a side-by-side look at the other version.
  let {
    label,
    value = $bindable(''),
    rows = 12,
    error = '',
    theirs = null,
    conflictText = 'Someone else changed this while you were editing.',
    busy = false,
    onsave,
    oncancel,
    onkeepmine,
    onusetheirs,
  }: {
    /** The field's name for screen readers; the heading above already shows it. */
    label: string;
    value?: string;
    rows?: number;
    error?: string;
    /** The saved version someone else wrote meanwhile; shows the conflict notice. */
    theirs?: string | null;
    conflictText?: string;
    busy?: boolean;
    onsave: () => void;
    oncancel: () => void;
    onkeepmine: () => void;
    onusetheirs: () => void;
  } = $props();

  const uid = $props.id();
  let area: HTMLTextAreaElement | undefined = $state();
  let compare = $state(false);

  onMount(() => area?.focus());

  const native = typeof CSS !== 'undefined' && typeof CSS.supports === 'function' && CSS.supports('field-sizing', 'content');
  $effect(() => {
    void value;
    if (native || !area) return;
    area.style.height = 'auto';
    area.style.height = `${area.scrollHeight + 2}px`;
  });

  function key(e: KeyboardEvent) {
    if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 's') {
      e.preventDefault();
      onsave();
    } else if (e.key === 'Escape') {
      e.preventDefault();
      oncancel();
    }
  }
</script>

{#if theirs !== null}
  <Notice tone="warning" class="mb-3" text={conflictText} action={{ label: 'See their version', onclick: () => (compare = true) }} />
{/if}
<label for="{uid}-text" class="sr-only">{label}</label>
<textarea
  bind:this={area}
  bind:value
  id="{uid}-text"
  class="input min-h-[var(--rows-h)] resize-none py-2 text-reading [field-sizing:content]"
  style:--rows-h="{rows * 24 + 18}px"
  spellcheck="true"
  aria-invalid={error ? 'true' : undefined}
  aria-describedby={error ? `${uid}-error` : undefined}
  onkeydown={key}
></textarea>
{#if error}
  <p id="{uid}-error" class="field-error"><Icon name="alert" class="mt-px" /><span class="min-w-0">{error}</span></p>
{/if}

<Dialog bind:open={compare} title="Their version and yours" size="lg" {busy}>
  <p class="mb-4 text-body text-muted">Struck-through text is only in their version. Underlined text is only in yours.</p>
  <DiffView mode="prose" diff={proseDiff(theirs ?? '', value)} />
  {#snippet footer()}
    <Button
      onclick={() => {
        compare = false;
        onusetheirs();
      }}>Use theirs</Button
    >
    <Button
      variant="primary"
      loading={busy}
      loadingLabel="Saving…"
      onclick={() => {
        compare = false;
        onkeepmine();
      }}>Keep mine</Button
    >
  {/snippet}
</Dialog>
