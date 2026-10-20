<script lang="ts">
  import { tick } from 'svelte';
  import Icon from '../../components/Icon.svelte';
  import { announce, Button, SettingsRow, StatusLine, toast, type RowControl } from '../../components/ui';
  import { api, ApiError } from '../../lib/api/client';
  import type { ChannelStatus, PictureAppResult, PictureChange, PictureInfo } from '../../lib/api/types.gen';
  import { pictureMaxBytes, pictureTooBig, pictureURL } from './picture';
  import SlackPictureNote from './SlackPictureNote.svelte';
  import { settingsConfig } from './store.svelte';

  const uid = $props.id();
  let info = $state<PictureInfo | null>(null);
  let loadFailed = $state(false);
  let slack = $state<ChannelStatus | undefined>();
  // Telegram or Discord is on, so Bothy's picture can be sent to them again.
  let sendable = $state(false);
  let picker: HTMLInputElement | undefined = $state();
  let busy = $state<'change' | 'reset' | null>(null);
  let error = $state('');
  // What the last change did in each chat app that is turned on.
  let apps = $state<PictureAppResult[]>([]);
  let last = $state<'change' | 'reset' | null>(null);

  async function load() {
    let failed = false;
    const [i, list] = await Promise.all([
      api.get<PictureInfo>('/api/agent/picture/info').catch((e: unknown) => {
        // A server without a picture says so with 503; the row is left out then.
        failed = !(e instanceof ApiError && e.status === 503 && e.code === 'unavailable');
        return null;
      }),
      api.get<ChannelStatus[]>('/api/channels').catch((): ChannelStatus[] => []),
    ]);
    info = i;
    loadFailed = failed;
    slack = list.find((c) => c.name === 'slack' && c.enabled);
    sendable = list.some((c) => (c.name === 'telegram' || c.name === 'discord') && c.enabled);
  }

  async function retryLoad() {
    await load();
    if (!info) return;
    await tick();
    document.getElementById(`${uid}-change`)?.focus();
  }

  $effect(() => {
    void load();
  });

  // Slack's step is the note below, whether or not a change just asked for it.
  const slackTodo = $derived(apps.some((a) => a.app === 'slack' && a.result === 'manual'));
  const lines = $derived(apps.filter((a) => a.result === 'failed' || (a.result === 'manual' && a.app !== 'slack')));
  // "Use default" goes with the admin's picture, so a reset some chat apps
  // missed is tried again from here.
  const resetAgain = $derived(last === 'reset' && apps.some((a) => a.result === 'failed'));
  const describedBy = (c: RowControl) => [c.labelId, c.describedBy].filter(Boolean).join(' ');

  async function change(files: FileList | null) {
    const f = files?.[0];
    if (picker) picker.value = '';
    if (!f) return;
    if (f.size > pictureMaxBytes) {
      apps = [];
      error = pictureTooBig;
      return;
    }
    const fd = new FormData();
    fd.append('file', f);
    await save('change', () => api.put<PictureChange>('/api/agent/picture', fd));
  }

  async function save(kind: 'change' | 'reset', call: () => Promise<PictureChange>) {
    const wasCustom = info?.custom;
    busy = kind;
    last = kind;
    error = '';
    apps = [];
    try {
      const r = await call();
      info = r.picture;
      apps = r.apps;
      // Sending the default again changes nothing in Bothy, so only a chat app that took it is news.
      if (kind === 'change') toast('Picture changed.');
      else if (wasCustom) toast('Default picture restored.');
      else if (apps.some((a) => a.result === 'updated')) toast('Picture sent to the chat apps.');
      if (lines.length) announce(lines.map((a) => a.detail).join(' '));
      // The ghost button can go with the custom picture; keep focus in the row.
      if (kind === 'reset' && !resetAgain && !sendable) {
        await tick();
        document.getElementById(`${uid}-change`)?.focus();
      }
    } catch (e) {
      // A refused file comes with the server's own sentence; a body over the cap may not.
      if (e instanceof ApiError && e.status === 413) error = pictureTooBig;
      else if (e instanceof ApiError && e.status === 400) error = e.message;
      else if (kind === 'change') error = "Couldn't change the picture. Try again.";
      else error = wasCustom ? "Couldn't go back to the default picture. Try again." : "Couldn't send the picture to the chat apps. Try again.";
    } finally {
      busy = null;
    }
  }
</script>

{#snippet note()}
  {#each lines as a (a.app)}
    <p class="flex items-start gap-1.5 text-warning-text"><Icon name="alert" class="mt-0.5" /><span class="min-w-0">{a.detail}</span></p>
  {/each}
  {#if slack || slackTodo}
    <div class={lines.length ? 'mt-1' : ''}><SlackPictureNote appId={slack?.app_id} todo={slackTodo} /></div>
  {/if}
{/snippet}

{#if info}
  {@const current = info}
  <SettingsRow label="Picture" help="Shown on Bothy's replies here and in the chat apps." {error} note={lines.length || slack || slackTodo ? note : undefined}>
    {#snippet children(c)}
      <!-- Small, iconless buttons: otherwise the second one wraps onto a line of its own in the 320 px column. -->
      <div class="flex flex-wrap items-center gap-2">
        <img src={pictureURL(current.version)} alt={settingsConfig.value<string>('agent.name', 'Bothy')} width="48" height="48" class="size-12 shrink-0 rounded-full border border-border bg-subtle" />
        <input
          bind:this={picker}
          type="file"
          accept="image/png,image/jpeg,image/webp"
          class="hidden"
          aria-hidden="true"
          tabindex="-1"
          onchange={(e) => change(e.currentTarget.files)}
        />
        <Button id="{uid}-change" size="sm" loading={busy === 'change'} loadingLabel="Saving…" disabled={busy === 'reset'} aria-describedby={describedBy(c)} onclick={() => picker?.click()}>Change…</Button>
        <!-- With the default in use, sending it again reaches a bot that wears an older one. -->
        {#if current.custom || resetAgain || sendable}
          <Button
            variant="ghost"
            size="sm"
            loading={busy === 'reset'}
            loadingLabel="Saving…"
            disabled={busy === 'change'}
            aria-describedby={describedBy(c)}
            onclick={() => save('reset', () => api.del<PictureChange>('/api/agent/picture'))}>{current.custom ? 'Use default' : resetAgain ? 'Try again' : 'Send to chat apps'}</Button
          >
        {/if}
      </div>
    {/snippet}
  </SettingsRow>
{:else if loadFailed}
  <SettingsRow label="Picture">
    {#snippet children()}
      <StatusLine tone="danger" text="The picture didn't load. Check your connection and try again." action={{ label: 'Try again', onclick: retryLoad }} />
    {/snippet}
  </SettingsRow>
{/if}
