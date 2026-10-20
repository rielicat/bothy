<script lang="ts">
  import { onMount, type Snippet } from 'svelte';
  import Icon from '../../components/Icon.svelte';
  import Button from '../../components/ui/Button.svelte';
  import Dialog from '../../components/ui/Dialog.svelte';
  import DiffView from '../../components/ui/DiffView.svelte';
  import Notice from '../../components/ui/Notice.svelte';
  import { proseDiff } from './text';

  // Editing notes or memory in place (§9.2, §9.4): a sans textarea that grows
  // with its Markdown source, ⌘S / Ctrl S to save, Esc to cancel, and the
  // edit-conflict notice. While a conflict is open the page's primary button
  // is "Compare versions", which opens `comparing`: saving again would only
  // meet the same conflict. On phones `actions` (Save and Cancel) sit under
  // the box, where they stay in reach while typing.
  let {
    label,
    value = $bindable(''),
    rows = 12,
    error = '',
    errorId,
    theirs = null,
    conflictText = 'Someone else changed this while you were editing.',
    busy = false,
    comparing = $bindable(false),
    actions,
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
    /** Set when the page shows `error` itself, next to Save: the field points at it instead of repeating it. */
    errorId?: string;
    /** The saved version someone else wrote meanwhile; shows the conflict notice. */
    theirs?: string | null;
    conflictText?: string;
    busy?: boolean;
    /** The compare dialog is open. */
    comparing?: boolean;
    /** The editing buttons, under the box: the page passes them on phones, where its header holds them otherwise. */
    actions?: Snippet;
    onsave: () => void;
    oncancel: () => void;
    onkeepmine: () => void;
    onusetheirs: () => void;
  } = $props();

  const uid = $props.id();
  let area: HTMLTextAreaElement | undefined = $state();

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
  <Notice tone="warning" class="mb-3" text={conflictText} />
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
  aria-describedby={error ? (errorId ?? `${uid}-error`) : undefined}
  onkeydown={key}
></textarea>
{#if error && !errorId}
  <p id="{uid}-error" class="field-error"><Icon name="alert" class="mt-px" /><span class="min-w-0">{error}</span></p>
{/if}
{#if actions}<div class="mt-3 flex gap-2">{@render actions()}</div>{/if}

<!-- Cancel only closes; replacing the draft is a deliberate button in the body, never the slot where Cancel usually sits. -->
<Dialog bind:open={comparing} title="Compare versions" size="lg" {busy} cancel lockCancel>
  <p class="mb-4 text-body text-muted">Struck-through text is only in their version. Underlined text is only in yours.</p>
  <DiffView mode="prose" diff={proseDiff(theirs ?? '', value)} />
  <Button
    class="mt-4"
    disabled={busy}
    onclick={() => {
      comparing = false;
      onusetheirs();
    }}>Use their version</Button
  >
  {#snippet footer()}
    <Button
      variant="primary"
      loading={busy}
      loadingLabel="Saving…"
      onclick={() => {
        comparing = false;
        onkeepmine();
      }}>Keep mine</Button
    >
  {/snippet}
</Dialog>
