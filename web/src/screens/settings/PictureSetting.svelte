<script lang="ts">
  import { tick } from 'svelte';
  import Icon from '../../components/Icon.svelte';
  import { announce, Button, SettingsRow, toast } from '../../components/ui';
  import { api, ApiError } from '../../lib/api/client';
  import type { ChannelStatus, PictureAppResult, PictureChange, PictureInfo } from '../../lib/api/types.gen';
  import { pictureMaxBytes, pictureTooBig, pictureURL } from './picture';
  import SlackPictureNote from './SlackPictureNote.svelte';
  import { settingsConfig } from './store.svelte';

  const uid = $props.id();
  let info = $state<PictureInfo | null>(null);
  let slack = $state<ChannelStatus | undefined>();
  let picker: HTMLInputElement | undefined = $state();
  let busy = $state<'change' | 'reset' | null>(null);
  let error = $state('');
  // What the last change did in each chat app that is turned on.
  let apps = $state<PictureAppResult[]>([]);

  async function load() {
    const [i, list] = await Promise.all([
      api.get<PictureInfo>('/api/agent/picture/info').catch(() => null),
      api.get<ChannelStatus[]>('/api/channels').catch((): ChannelStatus[] => []),
    ]);
    info = i;
    slack = list.find((c) => c.name === 'slack' && c.enabled);
  }

  $effect(() => {
    void load();
  });

  // Slack's step is the note below, whether or not a change just asked for it.
  const slackTodo = $derived(apps.some((a) => a.app === 'slack' && a.result === 'manual'));
  const lines = $derived(apps.filter((a) => a.result === 'failed' || (a.result === 'manual' && a.app !== 'slack')));

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
    busy = kind;
    error = '';
    apps = [];
    try {
      const r = await call();
      info = r.picture;
      apps = r.apps;
      toast(kind === 'change' ? 'Picture changed.' : 'Default picture restored.');
      if (lines.length) announce(lines.map((a) => a.detail).join(' '));
      // "Use default" goes away with the custom picture; keep focus in the row.
      if (kind === 'reset') {
        await tick();
        document.getElementById(`${uid}-change`)?.focus();
      }
    } catch (e) {
      // A refused file comes with the server's own sentence; a body over the cap may not.
      if (e instanceof ApiError && e.status === 413) error = pictureTooBig;
      else if (e instanceof ApiError && e.status === 400) error = e.message;
      else error = kind === 'change' ? "Couldn't change the picture. Try again." : "Couldn't go back to the default picture. Try again.";
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
  <SettingsRow label="Picture" help="Bothy uses it in the chat apps." {error} note={lines.length || slack || slackTodo ? note : undefined}>
    {#snippet children()}
      <div class="flex flex-wrap items-center gap-3">
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
        <Button id="{uid}-change" loading={busy === 'change'} loadingLabel="Saving…" disabled={busy === 'reset'} onclick={() => picker?.click()}>Change…</Button>
        {#if current.custom}
          <Button
            variant="ghost"
            loading={busy === 'reset'}
            loadingLabel="Saving…"
            disabled={busy === 'change'}
            onclick={() => save('reset', () => api.del<PictureChange>('/api/agent/picture'))}>Use default</Button
          >
        {/if}
      </div>
    {/snippet}
  </SettingsRow>
{/if}
