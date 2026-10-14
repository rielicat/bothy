<script lang="ts" module>
  export interface RowControl {
    /** Put this on the control so the row's label names it. */
    id: string;
    labelId: string;
    /** For aria-describedby on the control: the help or the error. */
    describedBy: string | undefined;
    invalid: boolean;
  }
</script>

<script lang="ts">
  import type { Snippet } from 'svelte';
  import Icon from '../Icon.svelte';

  let {
    label,
    help,
    for: forId,
    status = null,
    error,
    inline = false,
    note,
    children,
  }: {
    label: string;
    /** One line, only when it changes what you'd choose. */
    help?: string;
    /** The control's id when it brings its own; otherwise use the id the children snippet receives. */
    for?: string;
    status?: 'saving' | 'saved' | null;
    /** Replaces the help, under the control; the value was not saved. */
    error?: string;
    /** Keeps the control on the label line on phones too (switches). */
    inline?: boolean;
    /** A second line under the label, such as AdvancedField's "Changed · Reset to default". */
    note?: Snippet;
    children: Snippet<[RowControl]>;
  } = $props();

  const uid = $props.id();
  const controlId = $derived(forId ?? `${uid}-control`);
  const describedBy = $derived(error ? `${uid}-error` : help ? `${uid}-help` : undefined);

  // "Saved" shows for 2 s after a save, then the slot empties (the live region stays).
  let savedVisible = $state(false);
  $effect(() => {
    if (status !== 'saved') {
      savedVisible = false;
      return;
    }
    savedVisible = true;
    const t = setTimeout(() => (savedVisible = false), 2000);
    return () => clearTimeout(t);
  });
  const statusText = $derived(status === 'saving' ? 'Saving…' : savedVisible ? 'Saved' : '');
</script>

<div
  class="grid min-h-14 gap-x-6 gap-y-2 border-b border-border py-3 last:border-b-0 sm:grid-cols-[minmax(0,1fr)_minmax(0,20rem)] sm:items-center {inline
    ? 'grid-cols-[minmax(0,1fr)_auto] items-center'
    : ''}"
>
  <div class="min-w-0">
    <div class="flex flex-wrap items-baseline gap-x-2">
      <label id="{uid}-label" for={controlId} class="text-body font-medium text-text">{label}</label>
      <span class="text-small text-muted" aria-live="polite">{statusText}</span>
    </div>
    {#if help && !error}<p id="{uid}-help" class="mt-0.5 text-small text-muted">{help}</p>{/if}
    {#if note}<div class="mt-0.5 text-small text-muted">{@render note()}</div>{/if}
  </div>
  <div class="min-w-0 {inline ? 'justify-self-end' : ''}">
    {@render children({ id: controlId, labelId: `${uid}-label`, describedBy, invalid: !!error })}
    {#if error}
      <p id="{uid}-error" class="field-error"><Icon name="alert" class="mt-px" /><span class="min-w-0">{error}</span></p>
    {/if}
  </div>
</div>
