<script lang="ts">
  import type { Snippet } from 'svelte';
  import type { HTMLTextareaAttributes } from 'svelte/elements';
  import FieldMessage from './FieldMessage.svelte';

  type Props = Omit<HTMLTextareaAttributes, 'value' | 'class' | 'rows'> & {
    label: string;
    value?: string;
    help?: string | Snippet;
    error?: string;
    optional?: boolean;
    labelAction?: Snippet;
    rows?: number;
    /** Stops growing at this many lines and scrolls instead. */
    maxRows?: number;
    /** Monospace, for YAML and repeat rules only. */
    code?: boolean;
    textarea?: HTMLTextAreaElement;
    class?: string;
  };

  let {
    label,
    value = $bindable(''),
    help,
    error,
    optional = false,
    labelAction,
    rows = 3,
    maxRows,
    code = false,
    id: idProp,
    textarea = $bindable(),
    class: cls = '',
    ...rest
  }: Props = $props();

  const uid = $props.id();
  const id = $derived(idProp ?? `${uid}-input`);
  const describedBy = $derived(error ? `${uid}-error` : help ? `${uid}-help` : undefined);

  // Line heights of text-reading and text-code, plus 16 px padding and 2 px border.
  const line = $derived(code ? 20 : 24);
  const minHeight = $derived(rows * line + 18);
  const maxHeight = $derived(maxRows ? maxRows * line + 18 : undefined);

  const native = typeof CSS !== 'undefined' && typeof CSS.supports === 'function' && CSS.supports('field-sizing', 'content');

  // Fallback for browsers without field-sizing (Firefox, older Safari).
  $effect(() => {
    void value;
    if (native || !textarea) return;
    textarea.style.height = 'auto';
    textarea.style.height = `${Math.min(textarea.scrollHeight + 2, maxHeight ?? Infinity)}px`;
  });
</script>

<div class={cls}>
  <div class="flex items-baseline justify-between gap-3">
    <label for={id} class="field-label">{label}{#if optional}<span class="font-normal text-muted">{' (optional)'}</span>{/if}</label>
    {#if labelAction}<span class="text-small">{@render labelAction()}</span>{/if}
  </div>
  <textarea
    bind:this={textarea}
    bind:value
    {id}
    class="input mt-2 min-h-[var(--rows-h)] resize-none [field-sizing:content] pointer-coarse:min-h-[max(var(--rows-h),2.75rem)] {code ? 'font-mono text-code' : ''}"
    style:--rows-h="{minHeight}px"
    style:max-height={maxHeight ? `${maxHeight}px` : undefined}
    aria-invalid={error ? 'true' : undefined}
    aria-describedby={describedBy}
    {...rest}
  ></textarea>
  <FieldMessage id={uid} {help} {error} />
</div>
