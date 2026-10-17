<script lang="ts">
  import type { Snippet } from 'svelte';
  import { api, ApiError } from '../../lib/api/client';
  import type { UploadResp } from '../../lib/api/types.gen';
  import Button from '../../components/ui/Button.svelte';
  import FileToken from '../../components/ui/FileToken.svelte';
  import IconButton from '../../components/ui/IconButton.svelte';
  import ToggleButton from '../../components/ui/ToggleButton.svelte';

  // §7.5. Enter sends on a fine pointer and adds a line on touch; "/" opens
  // the commands; Esc stops a running task.
  let {
    running = false,
    paused = false,
    refine = $bindable(false),
    onrefine,
    refineLine = '',
    ensureSession,
    onsend,
    oncommand,
    onstop,
    picker,
  }: {
    running?: boolean;
    paused?: boolean;
    refine?: boolean;
    /** The person switched Refine for this chat. */
    onrefine?: (on: boolean) => void;
    /** The cost sentence shown while Refine is on. */
    refineLine?: string;
    /** The chat's id, creating the chat first when it is new (uploads need one). */
    ensureSession: () => Promise<string>;
    onsend: (text: string, uploads: string[]) => Promise<boolean>;
    oncommand: (cmd: string, arg: string) => void;
    onstop: () => void;
    /** The model picker, beside Send. */
    picker?: Snippet;
  } = $props();

  const MAX_MB = 20;
  const commands = [
    { cmd: '/new', desc: 'New chat', arg: false },
    { cmd: '/stop', desc: 'Stop', arg: false },
    { cmd: '/pin', desc: 'Keep a file in mind', arg: true },
    { cmd: '/model', desc: 'Change model', arg: true },
    { cmd: '/compact', desc: 'Summarise this chat so far', arg: false },
  ];

  interface Attached {
    key: number;
    name: string;
    size: number;
    id?: string;
    error?: string;
    job?: Promise<void>;
  }

  let text = $state('');
  let files: Attached[] = $state([]);
  let sending = $state(false);
  let index = $state(0);
  let ta: HTMLTextAreaElement | undefined = $state();
  let input: HTMLInputElement | undefined = $state();
  let seq = 0;
  const id = $props.id();

  const matches = $derived(/^\/\S*$/.test(text) ? commands.filter((c) => c.cmd.startsWith(text)) : []);
  const menu = $derived(matches.length > 0);
  const hasContent = $derived(!!text.trim() || files.some((f) => f.id || f.job));
  const errors = $derived(files.filter((f) => f.error).map((f) => f.error!));

  const coarse = () => !!window.matchMedia?.('(pointer: coarse)').matches;

  $effect(() => {
    if (index >= matches.length) index = 0;
  });

  export function focus() {
    ta?.focus();
  }

  /** Puts a suggestion in the box to edit; it is never sent from here. */
  export function fill(t: string) {
    text = t;
    ta?.focus();
    queueMicrotask(fit);
  }

  // field-sizing grows the box where supported; elsewhere it is measured.
  function fit() {
    if (!ta || CSS.supports?.('field-sizing', 'content')) return;
    ta.style.height = 'auto';
    if (ta.value) ta.style.height = `${Math.min(ta.scrollHeight, window.innerHeight * 0.4)}px`;
  }

  function pick(i: number) {
    const c = matches[i];
    if (!c) return;
    text = c.cmd + (c.arg ? ' ' : '');
    ta?.focus();
    if (!c.arg) void submit();
  }

  function keydown(e: KeyboardEvent) {
    if (menu) {
      if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
        e.preventDefault();
        index = (index + (e.key === 'ArrowDown' ? 1 : matches.length - 1)) % matches.length;
        return;
      }
      if (e.key === 'Tab' || (e.key === 'Enter' && !e.shiftKey)) {
        e.preventDefault();
        pick(index);
        return;
      }
      if (e.key === 'Escape') {
        e.preventDefault();
        text = '';
        return;
      }
    }
    if (e.key === 'Escape' && running) {
      e.preventDefault();
      onstop();
      return;
    }
    if (e.key === 'Enter' && !e.shiftKey && !e.isComposing && !coarse()) {
      e.preventDefault();
      void submit();
    }
  }

  async function submit() {
    const t = text.trim();
    const cmd = /^\/(pin|new|model|compact|stop)(?:\s+([\s\S]*))?$/.exec(t);
    if (cmd) {
      text = '';
      oncommand(cmd[1], (cmd[2] ?? '').trim());
      return;
    }
    if (paused || sending || (!t && !files.length)) return;
    sending = true;
    try {
      // Send waits for uploads still in flight.
      await Promise.all(files.map((f) => f.job));
      const ids = files.filter((f) => f.id).map((f) => f.id!);
      if (!t && !ids.length) return;
      if (await onsend(t, ids)) {
        text = '';
        files = [];
        queueMicrotask(fit);
      }
    } finally {
      sending = false;
      ta?.focus();
    }
  }

  export function addFiles(list: FileList | File[]) {
    for (const f of list) {
      const item: Attached = { key: ++seq, name: f.name, size: f.size };
      if (f.size > MAX_MB * 1024 * 1024) item.error = `${f.name} is larger than ${MAX_MB} MB.`;
      else item.job = upload(item, f);
      files.push(item);
    }
  }

  async function upload(item: Attached, f: File) {
    const at = () => files.find((x) => x.key === item.key);
    try {
      const fd = new FormData();
      fd.append('file', f);
      fd.append('session_id', await ensureSession());
      const r = await api.post<UploadResp>('/api/uploads', fd);
      const x = at();
      if (x) x.id = r.id;
    } catch (e) {
      const why = e instanceof ApiError && e.status === 413 ? `it's larger than ${MAX_MB} MB` : e instanceof ApiError && e.status === 415 ? "this file type isn't supported" : '';
      const x = at();
      if (x) x.error = why ? `Couldn't attach ${f.name}: ${why}.` : `Couldn't attach ${f.name}. Try again.`;
    } finally {
      const x = at();
      if (x) x.job = undefined;
    }
  }

  function paste(e: ClipboardEvent) {
    const list = e.clipboardData?.files;
    if (list?.length) {
      e.preventDefault();
      addFiles(list);
    }
  }
</script>

<div class="relative">
  {#if menu}
    <ul
      id="{id}-cmds"
      role="listbox"
      aria-label="Commands"
      data-floating
      class="absolute bottom-full left-0 mb-2 w-full max-w-80 rounded-lg border border-border bg-surface p-1 shadow-[var(--shadow-overlay)]"
    >
      {#each matches as c, i (c.cmd)}
        <li role="option" aria-selected={i === index} id="{id}-cmd-{i}">
          <button
            type="button"
            tabindex="-1"
            class="flex min-h-8 w-full cursor-pointer items-baseline gap-3 rounded-md px-2 py-1.5 text-left text-body pointer-coarse:min-h-11 {i === index ? 'bg-hover' : 'hover:bg-hover'}"
            onmousedown={(e) => e.preventDefault()}
            onclick={() => pick(i)}
          >
            <code class="w-16 shrink-0 font-mono text-code text-text">{c.cmd}</code><span class="text-muted">{c.desc}</span>
          </button>
        </li>
      {/each}
    </ul>
  {/if}

  <div
    class="rounded-lg border border-border-strong bg-surface p-3 transition-colors duration-120 has-[textarea:focus]:border-focus has-[textarea:focus]:shadow-[0_0_0_1px_var(--color-focus)]"
  >
    {#if files.length}
      <ul class="mb-2 flex flex-wrap gap-2" aria-label="Attached files">
        {#each files as f (f.key)}
          <li class="max-w-full min-w-0">
            <FileToken
              name={f.name}
              size={f.size}
              state={f.job ? 'uploading' : f.error ? 'error' : undefined}
              class={f.error ? 'text-danger-text' : ''}
              onremove={() => (files = files.filter((x) => x.key !== f.key))}
            />
          </li>
        {/each}
      </ul>
    {/if}
    <label for="{id}-ta" class="sr-only">Message</label>
    <textarea
      id="{id}-ta"
      bind:this={ta}
      bind:value={text}
      rows="1"
      disabled={paused}
      class="block max-h-[40vh] min-h-6 w-full resize-none bg-transparent text-reading text-text [field-sizing:content] placeholder:text-faint focus:outline-none disabled:cursor-not-allowed max-sm:text-[1rem] pointer-coarse:-my-2.5 pointer-coarse:py-2.5 pointer-coarse:text-[1rem]"
      placeholder={paused ? 'Bothy is paused' : running ? 'Add to the current task' : 'Message Bothy'}
      aria-autocomplete="list"
      aria-controls={menu ? `${id}-cmds` : undefined}
      aria-activedescendant={menu ? `${id}-cmd-${index}` : undefined}
      aria-describedby={paused ? `${id}-why` : undefined}
      onkeydown={keydown}
      oninput={fit}
      onpaste={paste}
    ></textarea>
    <div class="mt-2 flex items-center gap-1">
      <input
        bind:this={input}
        type="file"
        multiple
        class="hidden"
        tabindex="-1"
        aria-hidden="true"
        onchange={(e) => {
          addFiles(e.currentTarget.files ?? []);
          e.currentTarget.value = '';
        }}
      />
      <IconButton icon="plus" label="Attach files" size="sm" variant={paused ? 'quiet' : 'ghost'} disabled={paused} onclick={() => input?.click()} />
      <ToggleButton label="Refine" bind:pressed={refine} onchange={onrefine} tooltip="Bothy writes several drafts, checks them and sends the best one. Slower, and costs more." />
      <span class="flex-1"></span>
      {@render picker?.()}
      {#if running}
        <Button size="sm" icon="stop" onclick={onstop}>Stop</Button>
      {/if}
      {#if !running || hasContent}
        <IconButton
          icon="arrow-up"
          label="Send"
          size="sm"
          variant="primary"
          disabled={paused || !hasContent || sending}
          aria-describedby={paused ? `${id}-why` : undefined}
          onclick={submit}
        />
      {/if}
    </div>
  </div>
  {#each errors as e (e)}<p class="mt-2 text-small text-danger-text">{e}</p>{/each}
  {#if paused}
    <!-- The paused banner and the placeholder already say it; this names the reason for the disabled controls. -->
    <p id="{id}-why" class="sr-only">Bothy is paused.</p>
  {:else if refine && refineLine}
    <p class="mt-2 text-small text-muted">{refineLine}</p>
  {/if}
</div>
