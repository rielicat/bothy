<script lang="ts">
  import type { Component } from 'svelte';
  import Empty from '../../components/Empty.svelte';
  import Icon from '../../components/Icon.svelte';
  import { config } from '../../lib/config.svelte';
  import type { IconName } from '../../lib/icons';
  import { router } from '../../lib/router.svelte';
  import { session } from '../../lib/stores/session.svelte';
  import Budgets from './Budgets.svelte';
  import Channels from './Channels.svelte';
  import General from './General.svelte';
  import Mcp from './Mcp.svelte';
  import Models from './Models.svelte';
  import People from './People.svelte';
  import Persona from './Persona.svelte';
  import Skills from './Skills.svelte';
  import Tokens from './Tokens.svelte';

  let { params }: { params: Record<string, string> } = $props();

  type Page = { id: string; label: string; icon: IconName; c: Component; admin?: boolean; multi?: boolean };
  const pages: Page[] = [
    { id: 'general', label: 'General', icon: 'sliders', c: General },
    { id: 'models', label: 'Models & providers', icon: 'spark', c: Models, admin: true },
    { id: 'channels', label: 'Chat apps', icon: 'chat', c: Channels },
    { id: 'people', label: 'People', icon: 'users', c: People, admin: true, multi: true },
    { id: 'mcp', label: 'MCP servers', icon: 'plug', c: Mcp, admin: true },
    { id: 'skills', label: 'Skills', icon: 'skills', c: Skills },
    { id: 'persona', label: 'Persona', icon: 'smile', c: Persona },
    { id: 'tokens', label: 'API tokens', icon: 'code', c: Tokens },
    { id: 'budgets', label: 'Budgets', icon: 'wallet', c: Budgets, admin: true },
  ];

  const visible = $derived(pages.filter((p) => (!p.admin || session.isAdmin) && (!p.multi || session.multi)));
  const page = $derived(visible.find((p) => p.id === params.page));

  $effect(() => {
    if (session.isAdmin) void config.load();
  });
</script>

<svelte:head><title>{page?.label ?? 'Settings'} · Bothy</title></svelte:head>

<div class="mx-auto flex w-full max-w-6xl gap-8 px-4 py-6 sm:px-6 sm:py-8">
  <nav aria-label="Settings" class="hidden w-52 shrink-0 md:block">
    <p class="mb-3 px-3 text-lg font-semibold">Settings</p>
    <ul class="space-y-0.5">
      {#each visible as p (p.id)}
        <li>
          <a
            href="/settings/{p.id}"
            onclick={router.link}
            aria-current={p.id === params.page ? 'page' : undefined}
            class="flex items-center gap-2.5 rounded-lg px-3 py-1.5 text-sm text-muted hover:bg-sunken hover:text-fg aria-[current=page]:bg-sunken aria-[current=page]:font-medium aria-[current=page]:text-fg"
          >
            <Icon name={p.icon} size={16} />{p.label}
          </a>
        </li>
      {/each}
    </ul>
  </nav>

  <div class="min-w-0 flex-1">
    <label class="mb-4 block md:hidden">
      <span class="sr-only">Settings page</span>
      <select class="input" value={params.page} onchange={(e) => router.navigate(`/settings/${e.currentTarget.value}`)}>
        {#each visible as p (p.id)}<option value={p.id}>{p.label}</option>{/each}
      </select>
    </label>
    {#if !page}
      <Empty icon="sliders" title="No such settings page">
        {#snippet action()}<a class="btn" href="/settings/general" onclick={router.link}>General settings</a>{/snippet}
      </Empty>
    {:else}
      <h1 class="page-title mb-6">{page.label}</h1>
      {#if config.error && page.admin}<p class="alert-bad mb-4" role="alert">{config.error}</p>{/if}
      {#if config.view?.restart_required?.length && session.isAdmin}
        <p class="alert-info mb-4">Restart needed for: {config.view.restart_required.join(', ')}. Use Restart in the banner or under Advanced.</p>
      {/if}
      <page.c />
    {/if}
  </div>
</div>
