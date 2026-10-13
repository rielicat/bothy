<script lang="ts">
  import { api } from '../lib/api/client';
  import type { SandboxInfo } from '../lib/api/types.gen';
  import { ago } from '../lib/format';
  import { copyText } from '../lib/stores/ui.svelte';
  import Icon from './Icon.svelte';

  let { quota = false }: { quota?: boolean } = $props();

  let info = $state<SandboxInfo | null>(null);
  const st = $derived(info?.status);
  const fix = $derived(st?.fix || info?.compose_hint || '');
  const used = $derived(info && info.quota_mb ? Math.min(1, info.used_mb / info.quota_mb) : 0);

  $effect(() => {
    api.get<SandboxInfo>('/api/sandbox').then((i) => (info = i), () => {});
  });
</script>

{#if !st}
  <p class="py-4 text-sm text-muted">Checking the sandbox…</p>
{:else}
  <div class="space-y-3 py-4">
    <div class="flex items-center gap-3">
      <span class="grid size-10 place-items-center rounded-xl {st.available ? 'bg-ok-soft text-ok' : 'bg-bad-soft text-bad'}"><Icon name="box" size={20} /></span>
      <div class="min-w-0 flex-1">
        <p class="font-medium">{st.available ? `Code runs isolated with ${st.runtime}` : 'No sandbox: code tools stay off'}</p>
        <p class="text-xs text-muted">{st.backend}{st.engine ? ` · ${st.engine}` : ''}{st.rootless ? ' · rootless' : ''} · checked {ago(st.checked_at)}</p>
      </div>
    </div>
    {#if !st.available}
      <p class="alert-bad">{st.reason}</p>
      {#if fix}
        <div class="flex items-center gap-2">
          <code class="mono min-w-0 flex-1 overflow-x-auto rounded-md bg-sunken px-2 py-1.5 whitespace-nowrap">{fix}</code>
          <button type="button" class="btn btn-sm" onclick={() => copyText(fix)}>Copy</button>
        </div>
      {/if}
      <p class="text-xs text-muted">Chat, search and memory still work without it. Bothy never runs code on the host.</p>
    {/if}
    {#if st.self_test?.length}
      <ul class="grid gap-1.5 text-sm sm:grid-cols-2">
        {#each st.self_test as c, i (i)}
          <li class="flex items-center gap-2 {c.ok ? '' : 'text-bad'}"><Icon name={c.ok ? 'check' : 'x'} size={15} class={c.ok ? 'text-ok' : ''} />{c.name}{c.detail ? ` · ${c.detail}` : ''}</li>
        {/each}
      </ul>
    {/if}
    {#if quota && info?.quota_mb}
      <div>
        <p class="mb-1 text-xs text-muted">Your workspace: {info.used_mb} MB of {info.quota_mb} MB</p>
        <div class="h-2 overflow-hidden rounded-full bg-sunken"><div class="h-full rounded-full {used > 0.9 ? 'bg-bad' : 'bg-accent'}" style:width="{used * 100}%"></div></div>
      </div>
    {/if}
  </div>
{/if}
