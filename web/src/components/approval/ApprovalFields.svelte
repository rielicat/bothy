<script lang="ts">
  import type { Snippet } from 'svelte';
  import type { CopyField } from '../../lib/cardcopy';
  import CodeBlock from '../ui/CodeBlock.svelte';
  import DiffView from '../ui/DiffView.svelte';
  import LinkButton from '../ui/LinkButton.svelte';
  import Verbatim from '../Verbatim.svelte';

  // The card's key/value grid (§8.1). Values are verbatim and never cut; only
  // diffs and file contents fold behind "Show".
  let {
    fields,
    showOpen = false,
    votes,
  }: {
    fields: CopyField[];
    /** Start every "Show" open (Knowledge's suggested changes open the prose diff). */
    showOpen?: boolean;
    /** Renders the multi-approver Approvals row; the row is left out without it. */
    votes?: Snippet;
  } = $props();

  const uid = $props.id();
  let shown: Record<number, boolean> = $state({});
  const isOpen = (i: number) => shown[i] ?? showOpen;
</script>

<dl class="grid grid-cols-[6rem_minmax(0,1fr)] gap-x-4 gap-y-2 max-sm:grid-cols-1 max-sm:gap-y-0.5 max-sm:[&>dt:not(:first-child)]:mt-2">
  {#each fields as f, i (i)}
    {#if !f.votes || votes}
      <dt class="text-small text-muted sm:pt-px">{f.label}</dt>
      <dd class="min-w-0 text-body text-text">
        {#if f.votes}
          {@render votes?.()}
        {:else}
          {#if f.value !== undefined && !f.show}
            <span class="block whitespace-pre-wrap [overflow-wrap:anywhere] {f.mono ? 'font-mono text-code' : ''}" data-copy-ignore><Verbatim text={f.value} /></span>
          {/if}
          {#if f.show}
            <span class="whitespace-pre-wrap [overflow-wrap:anywhere]" data-copy-ignore
              >{#if f.summary}<span class="tabular-nums">{f.summary}</span>{' · '}{/if}</span
            ><LinkButton aria-expanded={isOpen(i)} aria-controls="{uid}-{i}" onclick={() => (shown[i] = !isOpen(i))}>{isOpen(i) ? 'Hide' : 'Show'}</LinkButton>
            {#if f.show.kind === 'files' && !isOpen(i)}
              <!-- Which files change stays in view while the diffs are folded (§8.6). -->
              <ul class="mt-1 space-y-0.5 text-small text-muted" data-copy-ignore>
                {#each f.show.files ?? [] as file (file.path)}
                  <li class="[overflow-wrap:anywhere]">
                    <span class="font-mono text-code text-text"><Verbatim text={file.path} /></span> · <span class="whitespace-nowrap tabular-nums">+{file.added} −{file.removed}</span>
                  </li>
                {/each}
              </ul>
            {/if}
          {/if}
          {#if f.link}
            {' · '}<a class="link" href={f.link.href} target="_blank" rel="noopener noreferrer">{f.link.label}<span aria-hidden="true">{' ↗'}</span><span class="sr-only">{' (opens in a new tab)'}</span></a>
          {/if}
        {/if}
      </dd>
      {#if f.show && isOpen(i)}
        <dd id="{uid}-{i}" class="col-span-2 min-w-0 max-sm:col-span-1 max-sm:mt-1">
          {#if f.show.kind === 'diff'}
            <DiffView diff={f.show.content} path={f.show.path} />
          {:else if f.show.kind === 'new-file'}
            <DiffView diff={f.show.content} mode="new-file" path={f.show.path} />
          {:else if f.show.kind === 'prose'}
            <DiffView diff={f.show.content} mode="prose" />
          {:else if f.show.kind === 'files'}
            <div class="space-y-2">
              {#each f.show.files ?? [] as file (file.path)}
                <DiffView diff={file.diff} path={file.path} />
              {/each}
            </div>
          {:else if f.show.kind === 'text'}
            <p class="text-body whitespace-pre-wrap [overflow-wrap:anywhere]" data-copy-ignore><Verbatim text={f.show.content} /></p>
          {:else}
            <CodeBlock code={f.show.content} lineNumbers={f.show.content.includes('\n')} maxLines={40} />
          {/if}
        </dd>
      {/if}
    {/if}
  {/each}
</dl>
