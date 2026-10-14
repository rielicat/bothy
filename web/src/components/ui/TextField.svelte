<script lang="ts">
  import type { Snippet } from 'svelte';
  import type { HTMLInputAttributes } from 'svelte/elements';
  import FieldMessage from './FieldMessage.svelte';

  type Props = Omit<HTMLInputAttributes, 'value' | 'type' | 'class' | 'prefix'> & {
    /** Leave out inside a SettingsRow, whose label names the field through the id it passes. */
    label?: string;
    value?: string;
    type?: 'text' | 'password' | 'email' | 'url' | 'search' | 'tel';
    help?: string | Snippet;
    /** Shown after the first submit, then live; replaces the help. */
    error?: string;
    /** Adds a muted " (optional)" to the label. */
    optional?: boolean;
    /** A fixed sign inside the field, such as "$". */
    prefix?: string;
    /** A small control at the end of the label line, such as Show / Hide. */
    labelAction?: Snippet;
    /** Shows the value as plain text instead of a field. */
    readonly?: boolean;
    input?: HTMLInputElement;
    class?: string;
  };

  let {
    label,
    value = $bindable(''),
    type = 'text',
    help,
    error,
    optional = false,
    prefix,
    labelAction,
    readonly = false,
    id: idProp,
    input = $bindable(),
    class: cls = '',
    ...rest
  }: Props = $props();

  const uid = $props.id();
  const id = $derived(idProp ?? `${uid}-input`);
  const describedBy = $derived(error ? `${uid}-error` : help ? `${uid}-help` : undefined);
</script>

<div class={cls}>
  {#if readonly}
    {#if label}<p class="field-label mb-2" id="{uid}-label">{label}</p>{/if}
    <p class="text-body break-words text-text">{value || '—'}</p>
  {:else}
    {#if label}
      <div class="mb-2 flex items-baseline justify-between gap-3">
        <label for={id} class="field-label">{label}{#if optional}<span class="font-normal text-muted">{' (optional)'}</span>{/if}</label>
        {#if labelAction}<span class="text-small">{@render labelAction()}</span>{/if}
      </div>
    {/if}
    <div class="relative">
      {#if prefix}
        <span class="pointer-events-none absolute inset-y-0 left-3 flex items-center text-body text-muted max-sm:text-[1rem] pointer-coarse:text-[1rem]" aria-hidden="true"
          >{prefix}</span
        >
      {/if}
      <input
        bind:this={input}
        bind:value
        {id}
        {type}
        class="input {prefix ? 'pl-7' : ''}"
        aria-invalid={error ? 'true' : undefined}
        aria-describedby={describedBy}
        {...rest}
      />
    </div>
    <FieldMessage id={uid} {help} {error} />
  {/if}
</div>
