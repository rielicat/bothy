<script lang="ts">
  import type { Snippet } from 'svelte';
  import type { Phrase } from '../../lib/activity';
  import Icon from '../Icon.svelte';
  import CodeBlock from '../ui/CodeBlock.svelte';
  import Disclosure from '../ui/Disclosure.svelte';
  import LinkButton from '../ui/LinkButton.svelte';
  import PhraseText from './PhraseText.svelte';

  // A system line in the transcript (§7.6): no box, at most one action, an
  // optional Details. A failure, or a stop the person can lift, sits on a
  // tint sized to its words, as chat apps show their notices; said once.
  let {
    text = '',
    phrase,
    error = false,
    tone,
    alert = false,
    action,
    details,
    detailsLabel = 'Details',
    more,
  }: {
    text?: string;
    /** Words with verbatim values (a command in mono) instead of plain text. */
    phrase?: Phrase;
    /** The same as tone 'danger'. */
    error?: boolean;
    tone?: 'warning' | 'danger';
    /** Read out once: only for an error that just followed something the person did. */
    alert?: boolean;
    action?: { label: string; onclick: () => void };
    /** The raw message behind Details, verbatim. */
    details?: string;
    detailsLabel?: string;
    /** Plain explanation behind the Details toggle instead of a raw message. */
    more?: Snippet;
  } = $props();

  const t = $derived(tone ?? (error ? 'danger' : undefined));
</script>

{#snippet words()}
  {#if phrase}<PhraseText {phrase} />{:else}{text}{/if}{#if action}{' '}<LinkButton onclick={action.onclick}>{action.label}</LinkButton>{/if}
{/snippet}

{#snippet behind(indent: string)}
  {#if details || more}
    <Disclosure label={detailsLabel} class={indent}>
      {#if more}{@render more()}{:else if details}<CodeBlock code={details} wrap />{/if}
    </Disclosure>
  {/if}
{/snippet}

{#if t}
  <div class="w-fit max-w-full rounded-md px-3 py-2 text-body text-text {t === 'danger' ? 'bg-danger-subtle' : 'bg-warning-subtle'}" role={alert ? 'alert' : undefined}>
    <p class="flex items-start gap-2">
      <span class="flex h-5 w-4 shrink-0 items-center justify-center"><Icon name="alert" class={t === 'danger' ? 'text-danger-text' : 'text-warning-text'} /></span>
      <span class="min-w-0">{@render words()}</span>
    </p>
    {@render behind('pl-6')}
  </div>
{:else}
  <div class="text-small text-muted">
    <p class="py-1">{@render words()}</p>
    {@render behind('')}
  </div>
{/if}
