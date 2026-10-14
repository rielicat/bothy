<script lang="ts" module>
  export type CheckResult =
    | { ok: true; message: string }
    | {
        ok: false;
        /** What happened and what to do, in plain words (§14.4). */
        message: string;
        /** The raw error, verbatim, behind Details. */
        details?: string;
        action?: { label: string; onclick: () => void };
      };

  export type KeyStatus = 'idle' | 'checking' | 'connected' | 'failed';
</script>

<script lang="ts">
  import type { Snippet } from 'svelte';
  import FieldMessage from './FieldMessage.svelte';
  import LinkButton from './LinkButton.svelte';
  import StatusLine from './StatusLine.svelte';

  let {
    label,
    value = $bindable(''),
    check,
    minLength = 20,
    checkingLabel = 'Checking…',
    help,
    saved = false,
    connectedAction,
    status = $bindable('idle'),
    onresult,
    placeholder,
    id: idProp,
    class: cls = '',
  }: {
    label: string;
    value?: string;
    /** Tests the key. Ignore the result once `signal` aborts: a newer value replaced it. */
    check: (value: string, signal: AbortSignal) => Promise<CheckResult>;
    /** Typing waits for this many characters before checking on its own. */
    minLength?: number;
    checkingLabel?: string;
    /** Shown while nothing has been checked, usually where to get a key. */
    help?: string | Snippet;
    /** A key is already stored; it is write-only, so only "Replace" is offered. */
    saved?: boolean;
    /** After the success sentence, such as "Change" for the model. */
    connectedAction?: { label: string; onclick: () => void };
    status?: KeyStatus;
    onresult?: (result: CheckResult) => void;
    placeholder?: string;
    id?: string;
    class?: string;
  } = $props();

  const uid = $props.id();
  const id = $derived(idProp ?? `${uid}-input`);
  const IDLE_MS = 800;

  let input: HTMLInputElement | undefined = $state();
  let reveal = $state(false);
  let replacing = $state(false);
  let result: CheckResult | null = $state(null);
  let byPerson = $state(false);
  let pasted = false;
  let timer: ReturnType<typeof setTimeout> | undefined;
  let ctrl: AbortController | null = null;

  $effect(() => () => {
    clearTimeout(timer);
    ctrl?.abort();
  });

  function cancel() {
    clearTimeout(timer);
    ctrl?.abort();
    ctrl = null;
  }

  /** Checks the current value now. Paste and Enter call it; so can a parent (bind:this). */
  export function checkNow() {
    cancel();
    const v = value.trim();
    if (!v) {
      status = 'idle';
      result = null;
      return;
    }
    const c = new AbortController();
    ctrl = c;
    status = 'checking';
    result = null;
    check(v, c.signal).then(
      (r) => {
        if (c.signal.aborted) return;
        result = r;
        status = r.ok ? 'connected' : 'failed';
        onresult?.(r);
      },
      (e: unknown) => {
        if (c.signal.aborted) return;
        const r: CheckResult = { ok: false, message: "Bothy couldn't check this key. Try again.", details: e instanceof Error ? e.message : String(e) };
        result = r;
        status = 'failed';
        onresult?.(r);
      },
    );
  }

  function oninput(e: Event) {
    cancel();
    status = 'idle';
    result = null;
    if (pasted || (e as InputEvent).inputType === 'insertFromPaste') {
      pasted = false;
      byPerson = true;
      checkNow();
      return;
    }
    if (value.trim().length >= minLength) {
      timer = setTimeout(() => {
        byPerson = false;
        checkNow();
      }, IDLE_MS);
    }
  }

  function onkeydown(e: KeyboardEvent) {
    if (e.key === 'Enter') {
      e.preventDefault();
      byPerson = true;
      checkNow();
    }
  }

  async function replace() {
    replacing = true;
    value = '';
    await Promise.resolve();
    input?.focus();
  }

  const statusId = `${uid}-status`;
  const describedBy = $derived(status === 'idle' ? (help ? `${uid}-help` : undefined) : statusId);
</script>

<div class={cls}>
  {#if saved && !replacing}
    <p class="field-label">{label}</p>
    <p class="mt-2 text-body text-text"><span aria-hidden="true">•••••</span><span class="sr-only">A key is</span> saved · <LinkButton onclick={replace}>Replace</LinkButton></p>
  {:else}
    <div class="flex items-baseline justify-between gap-3">
      <label for={id} class="field-label">{label}</label>
      <span class="text-small"
        ><LinkButton onclick={() => (reveal = !reveal)} aria-controls={id} aria-pressed={reveal}>{reveal ? 'Hide' : 'Show'}</LinkButton></span
      >
    </div>
    <input
      bind:this={input}
      bind:value
      {id}
      type={reveal ? 'text' : 'password'}
      class="input mt-2"
      autocomplete="off"
      autocapitalize="off"
      spellcheck="false"
      {placeholder}
      aria-invalid={status === 'failed' ? 'true' : undefined}
      aria-describedby={describedBy}
      onpaste={() => (pasted = true)}
      {oninput}
      {onkeydown}
    />
    {#if status === 'idle'}
      <FieldMessage id={uid} {help} />
    {:else if status === 'checking'}
      <StatusLine id={statusId} class="mt-2" tone="progress" text={checkingLabel} />
    {:else if result?.ok}
      <StatusLine id={statusId} class="mt-2" tone="success" text={result.message} action={connectedAction} />
    {:else if result}
      <StatusLine id={statusId} class="mt-2" tone="danger" text={result.message} details={result.details} action={result.action} alert={byPerson} />
    {/if}
  {/if}
</div>
