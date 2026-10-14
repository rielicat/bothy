<script lang="ts">
  import type { Snippet } from 'svelte';
  import type { Phrase } from '../../lib/activity';
  import Icon from '../Icon.svelte';
  import CodeBlock from '../ui/CodeBlock.svelte';
  import Disclosure from '../ui/Disclosure.svelte';
  import LinkButton from '../ui/LinkButton.svelte';
  import PhraseText from './PhraseText.svelte';

  // A system line in the transcript (§7.6): no box, at most one action, an
  // optional Details. Errors are red with the alert glyph and said once.
  let {
    text = '',
    phrase,
    error = false,
    alert = false,
    action,
    details,
    detailsLabel = 'Details',
    more,
  }: {
    text?: string;
    /** Words with verbatim values (a command in mono) instead of plain text. */
    phrase?: Phrase;
    error?: boolean;
    /** Read out once: only for an error that just followed something the person did. */
    alert?: boolean;
    action?: { label: string; onclick: () => void };
    /** The raw message behind Details, verbatim. */
    details?: string;
    detailsLabel?: string;
    /** Plain explanation behind the Details toggle instead of a raw message. */
    more?: Snippet;
  } = $props();
</script>

<div class="text-small {error ? 'text-danger-text' : 'text-muted'}" role={alert ? 'alert' : undefined}>
  <p class="flex items-start gap-2 py-1">
    {#if error}<span class="flex h-[1.125rem] w-4 shrink-0 items-center justify-center"><Icon name="alert" /></span>{/if}
    <span class="min-w-0">
      {#if phrase}<PhraseText {phrase} />{:else}{text}{/if}{#if action}{' '}<LinkButton onclick={action.onclick}>{action.label}</LinkButton>{/if}
    </span>
  </p>
  {#if details || more}
    <Disclosure label={detailsLabel} class={error ? 'pl-6' : ''}>
      {#if more}{@render more()}{:else if details}<CodeBlock code={details} wrap />{/if}
    </Disclosure>
  {/if}
</div>
