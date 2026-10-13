<script lang="ts">
  import Dialog from '../../components/Dialog.svelte';
  import Icon from '../../components/Icon.svelte';
  import Markdown from '../../components/Markdown.svelte';
  import Section from '../../components/Section.svelte';
  import { api } from '../../lib/api/client';
  import type { SkillDetail, SkillMeta } from '../../lib/api/types.gen';
  import { session } from '../../lib/stores/session.svelte';
  import { attempt, toasts } from '../../lib/stores/ui.svelte';

  let skills = $state<SkillMeta[]>([]);
  let view = $state<SkillDetail | null>(null);
  let picker = $state<HTMLInputElement>();

  const tone: Record<string, string> = { enabled: 'chip-ok', pending: 'chip-info', changed: 'chip-warn', disabled: '' };

  async function load() {
    skills = await api.get<SkillMeta[]>('/api/skills').catch(() => []);
  }

  $effect(() => {
    void load();
  });

  async function setEnabled(s: SkillMeta, enabled: boolean) {
    try {
      // Enabling names the hash shown here, so files rescanned since are refused.
      Object.assign(s, await api.patch<SkillMeta>(`/api/skills/${encodeURIComponent(s.name)}`, { enabled, hash: enabled ? s.hash : undefined }));
    } catch (e) {
      toasts.error(e);
    }
  }

  async function upload(files: FileList | null) {
    const f = files?.[0];
    if (!f) return;
    const fd = new FormData();
    fd.append('file', f);
    if (await attempt(() => api.post('/api/skills', fd), 'Uploaded. Review it, then enable it.')) void load();
    if (picker) picker.value = '';
  }

  async function remove(s: SkillMeta) {
    if (await attempt(() => api.del(`/api/skills/${encodeURIComponent(s.name)}`), `${s.name} removed.`)) skills = skills.filter((x) => x !== s);
  }
</script>

<Section title="Skills" desc="Instructions Bothy can load on demand. Skills add no tools and never run anything by themselves.">
  {#snippet actions()}
    {#if session.isAdmin}
      <input bind:this={picker} type="file" accept=".zip,application/zip" class="hidden" onchange={(e) => upload(e.currentTarget.files)} />
      <button type="button" class="btn btn-sm" onclick={() => picker?.click()}><Icon name="upload" size={14} />Upload zip</button>
      <button type="button" class="btn btn-sm" onclick={async () => { skills = (await api.post<SkillMeta[]>('/api/skills/rescan').catch(() => null)) ?? skills; }}><Icon name="refresh" size={14} />Rescan</button>
    {/if}
  {/snippet}
  {#each skills as s (s.name)}
    <div class="flex flex-wrap items-center gap-3 py-3.5">
      {#if session.isAdmin}
        <input type="checkbox" class="switch" checked={s.status === 'enabled'} aria-label="Enable {s.name}" onchange={(e) => setEnabled(s, e.currentTarget.checked)} />
      {/if}
      <div class="min-w-0 flex-1">
        <p class="font-medium">{s.name} <span class="chip {tone[s.status] ?? ''}">{s.status}</span></p>
        <p class="truncate text-sm text-muted">{s.description}</p>
        {#if s.status === 'changed'}<p class="text-xs text-warn">Its files changed since it was approved, so it is switched off until you review it.</p>{/if}
      </div>
      <span class="text-xs text-faint">{s.tokens} tokens</span>
      <button type="button" class="btn btn-sm" onclick={async () => (view = await api.get<SkillDetail>(`/api/skills/${encodeURIComponent(s.name)}`).catch(() => null))}>View</button>
      {#if session.isAdmin}<button type="button" class="btn btn-ghost btn-icon btn-sm" aria-label="Delete {s.name}" onclick={() => remove(s)}><Icon name="trash" size={14} /></button>{/if}
    </div>
  {:else}
    <p class="py-6 text-center text-sm text-muted">No skills installed. Upload a zip with a SKILL.md, or add a folder to the skills directory.</p>
  {/each}
</Section>

<Dialog open={!!view} title={view?.meta?.name ?? 'Skill'} kind="sheet" onclose={() => (view = null)}>
  {#if view}
    <p class="mb-3 text-sm text-muted">Read-only. Files: {view.files.join(', ')}</p>
    <div class="rounded-xl border border-line p-4"><Markdown text={view.body} /></div>
  {/if}
</Dialog>
