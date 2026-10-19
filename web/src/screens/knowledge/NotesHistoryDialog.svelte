<script lang="ts" module>
  export interface HistoryEntry {
    key: string | number;
    /** "Bothy", "You", "Priya Rao (admin)". */
    who: string;
    /** "added 3 lines", "edited". */
    what: string;
    at: string;
    /** Which document, when the history covers more than one. */
    section?: string;
    chat?: { id: string; title: string };
    /** "Bothy in “Fix login redirect”" (memory) rather than "… · from “Login bug”" (notes). */
    chatIn?: boolean;
    before: string;
    after: string;
  }
</script>

<script lang="ts">
  import Button from '../../components/ui/Button.svelte';
  import Dialog from '../../components/ui/Dialog.svelte';
  import DiffView from '../../components/ui/DiffView.svelte';
  import LinkButton from '../../components/ui/LinkButton.svelte';
  import { proseDiff } from './text';
  import { fullTime, relTime } from '../../lib/format';
  import { router } from '../../lib/router.svelte';
  import Loading from './Loading.svelte';

  // Who changed a document and how (§9.2, §9.4): one line per change, its prose diff on request.
  let { open = $bindable(false), entries, failed = false, onretry }: { open?: boolean; entries: HistoryEntry[] | null; failed?: boolean; onretry?: () => void } = $props();

  let shown = $state<Record<string, boolean>>({});
</script>

{#snippet chatLink(c: { id: string; title: string })}<a class="link" href="/chat/{encodeURIComponent(c.id)}" onclick={(e) => ((open = false), router.link(e))}
    >“{c.title}”</a
  >{/snippet}

<Dialog bind:open title="History" size="lg">
  {#if !entries}
    <Loading {failed} {onretry} text="The history didn't load. Check your connection and try again." />
  {:else if !entries.length}
    <p class="text-reading text-text">No changes yet.</p>
  {:else}
    <ul>
      {#each entries as e (e.key)}
        <li class="border-b border-border py-3 first:pt-0 last:border-b-0">
          <p class="text-body text-text">
            <span class="font-medium">{e.who}</span>{#if e.chat && e.chatIn}{' in '}{@render chatLink(e.chat)}{/if}
            <span class="text-muted"
              >{' · '}{e.what}{' · '}<time datetime={e.at} title={fullTime(e.at)}>{relTime(e.at)}</time>{#if e.section}{' · '}{e.section}{/if}{#if e.chat && !e.chatIn}{' · from '}{@render chatLink(
                  e.chat,
                )}{/if}{' · '}</span
            ><LinkButton aria-expanded={!!shown[e.key]} onclick={() => (shown = { ...shown, [e.key]: !shown[e.key] })}>{shown[e.key] ? 'Hide' : 'Show'}</LinkButton>
          </p>
          {#if shown[e.key]}
            <DiffView class="mt-3" mode="prose" diff={proseDiff(e.before, e.after)} />
          {/if}
        </li>
      {/each}
    </ul>
  {/if}
  <!-- On phones the sheet has no close button of its own; this one sits in its header bar. -->
  {#snippet footer({ close })}<Button onclick={close}>Close</Button>{/snippet}
</Dialog>
