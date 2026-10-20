<script lang="ts">
  import Button from '../../components/ui/Button.svelte';
  import EmptyState from '../../components/ui/EmptyState.svelte';
  import IconButton from '../../components/ui/IconButton.svelte';
  import ListRow from '../../components/ui/ListRow.svelte';
  import Menu from '../../components/ui/Menu.svelte';
  import Switch from '../../components/ui/Switch.svelte';
  import { toast } from '../../components/ui/toast.svelte';
  import Icon from '../../components/Icon.svelte';
  import { api, ApiError } from '../../lib/api/client';
  import type { SkillMeta } from '../../lib/api/types.gen';
  import { skillName } from '../../lib/format';
  import { session } from '../../lib/stores/session.svelte';
  import Knowledge from './Knowledge.svelte';
  import Loading from '../../components/ui/Loading.svelte';

  let skills = $state<SkillMeta[] | null>(null);
  let failed = $state(false);
  let error = $state('');
  let picker: HTMLInputElement | undefined = $state();
  let uploading = $state(false);

  const admin = $derived(session.isAdmin);
  const href = (s: SkillMeta) => `/knowledge/skills/${encodeURIComponent(s.name)}`;
  // A new or changed skill stays off until an admin approves its files.
  const needsReview = (s: SkillMeta) => s.status === 'changed' || s.status === 'pending';

  async function load() {
    failed = false;
    try {
      skills = await api.get<SkillMeta[]>('/api/skills');
    } catch {
      failed = true;
    }
  }

  $effect(() => {
    void load();
  });

  async function setOn(s: SkillMeta, on: boolean) {
    error = '';
    const was = s.status;
    s.status = on ? 'enabled' : 'disabled';
    try {
      // Turning on names the files it shows, so files changed since are refused.
      Object.assign(s, await api.patch<SkillMeta>(`/api/skills/${encodeURIComponent(s.name)}`, { enabled: on, hash: on ? s.hash : undefined }));
    } catch (e) {
      s.status = was;
      error =
        e instanceof ApiError && e.status === 409
          ? `The files of ${skillName(s.name)} changed. Review it before turning it on.`
          : `Couldn't turn ${on ? 'on' : 'off'} ${skillName(s.name)}. Try again.`;
      void load();
    }
  }

  async function upload(files: FileList | null) {
    const f = files?.[0];
    if (picker) picker.value = '';
    if (!f) return;
    error = '';
    uploading = true;
    const fd = new FormData();
    fd.append('file', f);
    try {
      await api.post('/api/skills', fd);
      toast('Skill added.');
      void load();
    } catch {
      error = "Couldn't add that skill. Check that it's a zip file with a SKILL.md inside.";
    } finally {
      uploading = false;
    }
  }

  async function rescan() {
    error = '';
    try {
      skills = await api.post<SkillMeta[]>('/api/skills/rescan');
      toast('Checked for changes.');
    } catch {
      error = "Couldn't check for changes. Try again.";
    }
  }
</script>

{#snippet actions()}
  {#if admin}
    <input bind:this={picker} type="file" accept=".zip,application/zip" class="hidden" aria-hidden="true" tabindex="-1" onchange={(e) => upload(e.currentTarget.files)} />
    <!-- An empty list offers "Add skill" itself; a second button here would only repeat it. -->
    {#if skills?.length}<Button loading={uploading} loadingLabel="Adding…" onclick={() => picker?.click()}>Add skill</Button>{/if}
    <Menu items={[{ label: 'Check for changes', onselect: rescan }]} placement="bottom-end">
      {#snippet trigger(p)}<IconButton {...p} icon="more" label="Skill options" />{/snippet}
    </Menu>
  {/if}
{/snippet}

<Knowledge title="Skills" {actions}>
  {#if error}<p class="field-error mb-4" role="alert"><Icon name="alert" class="mt-px" /><span>{error}</span></p>{/if}
  {#if !skills}
    <Loading {failed} retry={load} />
  {:else if !skills.length}
    <EmptyState
      text="No skills yet."
      more="Skills teach Bothy how your team does specific things, like writing release notes."
      action={admin ? { label: 'Add skill', onclick: () => picker?.click() } : undefined}
    />
  {:else}
    <div class="list-card overflow-hidden">
      {#each skills as s (s.name)}
        <ListRow href={href(s)} title={skillName(s.name)} description={s.description}>
          {#snippet trailing()}
            <!-- A skill waiting for review is off: admins get the one way to act on it, members see it as off. -->
            {#if needsReview(s) && admin}
              <Button size="sm" href={href(s)}>Review</Button>
            {:else if admin}
              <Switch label={skillName(s.name)} hideLabel bind:checked={() => s.status === 'enabled', (on) => setOn(s, on)} />
            {:else}
              <span class="pointer-events-none flex items-center gap-3 text-small text-muted">{!needsReview(s) && s.status === 'enabled' ? 'On' : 'Off'}<Icon name="chevron-right" /></span>
            {/if}
          {/snippet}
        </ListRow>
      {/each}
    </div>
  {/if}
</Knowledge>
