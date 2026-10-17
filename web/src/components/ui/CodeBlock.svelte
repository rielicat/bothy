<script lang="ts">
  import Verbatim from '../Verbatim.svelte';
  import IconButton from './IconButton.svelte';
  import LinkButton from './LinkButton.svelte';
  import { announce } from './live.svelte';

  let {
    code,
    wrap = false,
    copy = true,
    maxLines,
    lineNumbers = false,
    label,
    command = false,
    class: cls = '',
  }: {
    code: string;
    /** Wraps long lines instead of scrolling sideways. */
    wrap?: boolean;
    copy?: boolean;
    /** Shows this many lines first, then "Show all {n} lines". */
    maxLines?: number;
    lineNumbers?: boolean;
    /** A caption above the block: "Command". */
    label?: string;
    /** A command to run: Copy always shows, and lines break only between words. */
    command?: boolean;
    class?: string;
  } = $props();

  const uid = $props.id();
  const lines = $derived(code.replace(/\n$/, '').split('\n'));
  let all = $state(false);
  const clipped = $derived(!!maxLines && !all && lines.length > maxLines);
  const visible = $derived(clipped ? lines.slice(0, maxLines) : lines);

  let pre: HTMLPreElement | undefined = $state();
  let copied = $state(false);
  let timer: ReturnType<typeof setTimeout> | undefined;

  async function doCopy() {
    try {
      await navigator.clipboard.writeText(code);
      copied = true;
      announce('Copied');
      clearTimeout(timer);
      timer = setTimeout(() => (copied = false), 1500);
    } catch {
      // No clipboard (plain http, or permission denied): select the text so ⌘C / Ctrl C works.
      if (pre) window.getSelection()?.selectAllChildren(pre);
    }
  }
</script>

<div class="min-w-0 {cls}">
  {#if label}<p id="{uid}-label" class="mb-1 text-small text-muted">{label}</p>{/if}
  <div class="group relative">
    <!-- A block that scrolls sideways takes focus so the keyboard can scroll it. -->
    <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
    <pre
      bind:this={pre}
      class="rounded-md bg-subtle p-3 text-code text-text {wrap ? 'whitespace-pre-wrap [overflow-wrap:anywhere]' : 'overflow-x-auto'} {copy
        ? 'pr-11 pointer-coarse:pr-14'
        : ''}"
      role={label ? 'group' : undefined}
      aria-labelledby={label ? `${uid}-label` : undefined}
      tabindex={wrap ? undefined : 0}
      data-copy-ignore><code
        >{#each visible as l, i (i)}<span class="flex"
            >{#if lineNumbers}<span class="w-8 shrink-0 pr-3 text-right text-muted select-none" aria-hidden="true">{i + 1}</span>{/if}<span class="min-w-0"
              >{#if command && l}{#each l.split(/( +)/) as w, j (j)}{#if j % 2}{w}{:else if w}<span class="inline-block max-w-full"
                      ><Verbatim text={w} /></span
                    >{/if}{/each}{:else}<Verbatim text={l || ' '} />{/if}</span
            ></span
          >{/each}</code
      ></pre>
    {#if copy}
      <div class="absolute top-1 right-1 flex items-center gap-1 pointer-coarse:top-0 pointer-coarse:right-0">
        {#if copied}<span class="text-small text-muted">Copied</span>{/if}
        <IconButton
          icon={copied ? 'check' : 'copy'}
          label="Copy"
          size="sm"
          variant="quiet"
          class="{copied || command ? '' : 'opacity-0'} group-focus-within:opacity-100 group-hover:opacity-100 pointer-coarse:opacity-100"
          onclick={doCopy}
        />
      </div>
    {/if}
  </div>
  {#if clipped}
    <LinkButton class="mt-1 text-small" onclick={() => (all = true)}>Show all {lines.length} lines</LinkButton>
  {/if}
</div>
