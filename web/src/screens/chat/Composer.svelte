<script lang="ts">
  import Icon from '../../components/Icon.svelte';
  import { api } from '../../lib/api/client';
  import type { UploadResp } from '../../lib/api/types.gen';
  import { bytes } from '../../lib/format';
  import { toasts } from '../../lib/stores/ui.svelte';

  let {
    sessionId,
    running = false,
    paused = false,
    onsend,
    oncommand,
    onstop,
  }: {
    sessionId: string;
    running?: boolean;
    paused?: boolean;
    onsend: (text: string, uploads: string[]) => Promise<boolean>;
    oncommand: (cmd: string, arg: string) => void;
    onstop: () => void;
  } = $props();

  const commands = [
    { cmd: '/pin', arg: 'text', desc: 'Pin an instruction to this chat' },
    { cmd: '/new', arg: '', desc: 'Start a new chat' },
    { cmd: '/model', arg: 'provider/model', desc: 'Switch model for this chat (empty resets)' },
    { cmd: '/compact', arg: '', desc: 'Summarize older messages now' },
    { cmd: '/stop', arg: '', desc: 'Stop the current run' },
  ];

  let text = $state('');
  let files: UploadResp[] = $state([]);
  let uploading = $state(0);
  let sending = $state(false);
  let index = $state(0);
  let ta: HTMLTextAreaElement;
  let picker: HTMLInputElement;
  const id = $props.id();

  const matches = $derived(/^\/\S*$/.test(text) ? commands.filter((c) => c.cmd.startsWith(text)) : []);
  const menu = $derived(matches.length > 0);

  // Sized on input rather than in an effect: at mount the textarea may not be
  // laid out yet and would measure its placeholder at zero width. CSSOM writes
  // are allowed under style-src 'self'; only style attributes in markup are not.
  function fit() {
    ta.style.height = 'auto';
    if (ta.value) ta.style.height = `${Math.min(ta.scrollHeight, 240)}px`;
  }

  $effect(() => {
    if (index >= matches.length) index = 0;
  });

  function pick(i: number) {
    const c = matches[i];
    if (!c) return;
    text = c.cmd + (c.arg ? ' ' : '');
    ta.focus();
    queueMicrotask(fit);
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
        text = '';
        return;
      }
    }
    if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) {
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
    if ((!t && !files.length) || uploading || sending) return;
    sending = true;
    try {
      if (await onsend(t, files.map((f) => f.id))) {
        text = '';
        files = [];
        queueMicrotask(fit);
      }
    } finally {
      sending = false;
      ta?.focus();
    }
  }

  async function upload(list: FileList | File[]) {
    for (const f of list) {
      uploading++;
      const fd = new FormData();
      fd.append('file', f);
      fd.append('session_id', sessionId);
      try {
        files.push(await api.post<UploadResp>('/api/uploads', fd));
      } catch (e) {
        toasts.error(e);
      } finally {
        uploading--;
      }
    }
  }

  function paste(e: ClipboardEvent) {
    const list = e.clipboardData?.files;
    if (list?.length) {
      e.preventDefault();
      void upload(list);
    }
  }
</script>

<div class="relative">
  {#if menu}
    <ul
      id="{id}-cmds"
      class="absolute bottom-full left-0 mb-2 w-full max-w-sm overflow-hidden rounded-xl border border-line bg-surface py-1 shadow-lg"
      role="listbox"
      aria-label="Commands"
    >
      {#each matches as c, i (c.cmd)}
        <li role="option" aria-selected={i === index}>
          <button
            type="button"
            tabindex="-1"
            class="flex w-full items-baseline gap-2 px-3 py-1.5 text-left text-sm hover:bg-sunken {i === index ? 'bg-sunken' : ''}"
            onmousedown={(e) => e.preventDefault()}
            onclick={() => pick(i)}
          >
            <code class="font-mono font-medium">{c.cmd}</code>{#if c.arg}<span class="text-xs text-faint">{c.arg}</span>{/if}
            <span class="ml-auto truncate text-xs text-muted">{c.desc}</span>
          </button>
        </li>
      {/each}
    </ul>
  {/if}

  <div class="rounded-2xl border border-line bg-surface shadow-sm transition-colors focus-within:border-accent">
    {#if files.length || uploading}
      <ul class="flex flex-wrap gap-1.5 px-3 pt-3">
        {#each files as f, i (f.id)}
          <li class="chip !pr-1">
            <Icon name="file" size={13} /><span class="max-w-40 truncate">{f.name}</span><span class="text-faint">{bytes(f.size)}</span>
            <button type="button" class="rounded-full p-0.5 hover:bg-sunken" aria-label="Remove {f.name}" onclick={() => files.splice(i, 1)}><Icon name="x" size={12} /></button>
          </li>
        {/each}
        {#if uploading}<li class="chip animate-pulse">Uploading…</li>{/if}
      </ul>
    {/if}
    <label for="{id}-ta" class="sr-only">Message</label>
    <textarea
      id="{id}-ta"
      bind:this={ta}
      bind:value={text}
      rows="1"
      class="block max-h-60 w-full resize-none bg-transparent px-4 pt-3 pb-1 text-[15px] leading-relaxed placeholder:text-faint focus:outline-none"
      placeholder={paused ? 'Bothy is paused by an admin' : running ? 'Add to the current task…' : 'Message Bothy…  (/ for commands)'}
      aria-autocomplete="list"
      aria-controls={menu ? `${id}-cmds` : undefined}
      onkeydown={keydown}
      oninput={fit}
      onpaste={paste}
    ></textarea>
    <div class="flex items-center gap-1 px-2 pb-2">
      <input
        bind:this={picker}
        type="file"
        multiple
        class="hidden"
        tabindex="-1"
        aria-label="Attach files"
        onchange={async (e) => {
          await upload(e.currentTarget.files ?? []);
          picker.value = '';
        }}
      />
      <button type="button" class="btn btn-ghost btn-icon btn-sm" aria-label="Attach files" title="Attach files" onclick={() => picker.click()}>
        <Icon name="clip" size={17} />
      </button>
      <span class="flex-1 truncate px-1 text-xs text-faint max-sm:hidden">
        {running ? 'Messages you send now are added to the running task.' : 'Enter to send · Shift+Enter for a new line'}
      </span>
      <span class="flex-1 sm:hidden"></span>
      {#if running}
        <button type="button" class="btn btn-sm" onclick={onstop} title="Stop the run"><Icon name="stop" size={14} />Stop</button>
      {/if}
      <button
        type="button"
        class="btn btn-primary btn-icon btn-sm !rounded-full"
        aria-label="Send"
        disabled={sending || !!uploading || (!text.trim() && !files.length)}
        onclick={submit}
      >
        <Icon name="up" size={16} />
      </button>
    </div>
  </div>
</div>
