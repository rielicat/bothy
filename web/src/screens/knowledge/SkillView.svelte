<script lang="ts">
  import Icon from '../../components/Icon.svelte';
  import Markdown from '../../components/Markdown.svelte';
  import CodeBlock from '../../components/ui/CodeBlock.svelte';
  import Disclosure from '../../components/ui/Disclosure.svelte';
  import EmptyState from '../../components/ui/EmptyState.svelte';
  import LinkButton from '../../components/ui/LinkButton.svelte';
  import Notice from '../../components/ui/Notice.svelte';
  import PageHeader from '../../components/ui/PageHeader.svelte';
  import { toast } from '../../components/ui/toast.svelte';
  import { api, ApiError } from '../../lib/api/client';
  import type { SkillDetail } from '../../lib/api/types.gen';
  import { skillName } from '../../lib/format';
  import { session } from '../../lib/stores/session.svelte';
  import Loading from './Loading.svelte';

  let { params }: { params: Record<string, string> } = $props();
  const name = $derived(params.name);

  let skill = $state<SkillDetail | null>(null);
  let missing = $state(false);
  let failed = $state(false);
  let approveError = $state('');
  let files = $state<Record<string, string | null>>({});
  let open = $state<Record<string, boolean>>({});

  const meta = $derived(skill?.meta);
  const review = $derived(meta?.status === 'changed' || meta?.status === 'pending');
  const title = $derived(skillName(name, skill?.body ?? ''));
  // The page shows the instructions: the front matter (in the SKILL.md file below) and a first heading that repeats the title go.
  const body = $derived(
    (skill?.body ?? '')
      .replace(/^---\n[\s\S]*?\n---\n/, '')
      .replace(/^\s*#\s+(.+)\n/, (line, h: string) => ([name.toLowerCase(), title.toLowerCase()].includes(h.trim().toLowerCase()) ? '' : line)),
  );
  const path = (f: string) => `/api/skills/${encodeURIComponent(name)}/files/${f.split('/').map(encodeURIComponent).join('/')}`;

  async function load() {
    failed = missing = false;
    try {
      skill = await api.get<SkillDetail>(`/api/skills/${encodeURIComponent(name)}`);
    } catch (e) {
      if (e instanceof ApiError && e.status === 404) missing = true;
      else failed = true;
    }
  }

  $effect(() => {
    void name;
    skill = null;
    files = {};
    open = {};
    void load();
  });

  async function approve() {
    if (!meta) return;
    approveError = '';
    try {
      await api.patch(`/api/skills/${encodeURIComponent(name)}`, { enabled: true, hash: meta.hash });
      toast(meta.status === 'changed' ? 'Approved.' : 'Skill turned on.');
      void load();
    } catch {
      approveError = "Couldn't turn it on. The files may have changed again, so reload and review them.";
    }
  }

  function toggle(f: string) {
    open = { ...open, [f]: !open[f] };
    if (open[f] && files[f] == null) void fetchFile(f);
  }

  // A file that didn't load (null) is asked for again on Try again or when it's opened again.
  async function fetchFile(f: string) {
    delete files[f];
    const text = f === 'SKILL.md' ? (skill?.body ?? '') : await api.text(path(f)).catch(() => null);
    files = { ...files, [f]: text };
  }
</script>

<svelte:head><title>{skill ? title : skillName(name)} · Bothy</title></svelte:head>

<div class="page">
  <PageHeader title={skill ? title : skillName(name)} back={{ href: '/knowledge/skills', label: 'Skills' }} />
  <div class="mt-6">
    {#if missing}
      <EmptyState text="This skill doesn't exist or was removed." action={{ label: 'Go to Skills', href: '/knowledge/skills', variant: 'secondary' }} />
    {:else if !skill}
      <Loading {failed} onretry={load} />
    {:else}
      {#if review}
        <Notice
          tone="warning"
          class="mb-6"
          text={meta?.status === 'changed' ? "This skill changed, so it's off until an admin approves the changes below." : "This skill is new, so it's off until an admin reviews it."}
          action={session.isAdmin ? { label: meta?.status === 'changed' ? 'Approve' : 'Turn on', onclick: approve } : undefined}
        />
        {#if approveError}<p class="field-error -mt-4 mb-6" role="alert"><Icon name="alert" class="mt-px" /><span>{approveError}</span></p>{/if}
      {/if}
      <Markdown text={body} />
      <!-- While the changes wait for approval the files start open, so they are seen before approving. -->
      <Disclosure class="mt-10" label="Files" variant="section" open={review}>
        <!-- The disclosure's panel is a grid item that would grow to the longest line of an open file;
             without an intrinsic width of its own the list stays in the column and the file scrolls instead. -->
        <ul class="[contain:inline-size]">
          {#each skill.files as f (f)}
            <li class="border-b border-border last:border-b-0">
              <button
                type="button"
                class="flex min-h-10 w-full cursor-pointer items-center gap-2 py-2 text-left text-text pointer-coarse:min-h-12"
                aria-expanded={!!open[f]}
                onclick={() => toggle(f)}
                ><Icon name="chevron-right" size={12} class="shrink-0 text-muted transition-transform duration-160 {open[f] ? 'rotate-90' : ''}" /><span class="min-w-0 font-mono text-code [overflow-wrap:anywhere]" data-path
                  >{f}</span
                ></button
              >
              {#if open[f]}
                <div class="pb-3">
                  {#if !(f in files)}
                    <Loading />
                  {:else if files[f] === null}
                    <p class="text-body text-muted">
                      This file didn't load. Check your connection and try again.{' '}<LinkButton onclick={() => fetchFile(f)}>Try again</LinkButton>
                    </p>
                  {:else}
                    <CodeBlock code={files[f] ?? ''} lineNumbers maxLines={40} />
                  {/if}
                </div>
              {/if}
            </li>
          {/each}
        </ul>
      </Disclosure>
    {/if}
  </div>
</div>
