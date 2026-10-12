<script lang="ts">
  import { router } from './lib/router.svelte';
  import { events } from './lib/sse.svelte';

  const nav = [
    { href: '/chat', base: '/chat', label: 'Chat' },
    { href: '/approvals', base: '/approvals', label: 'Approvals' },
    { href: '/schedules', base: '/schedules', label: 'Schedules' },
    { href: '/memory', base: '/memory', label: 'Memory' },
    { href: '/usage', base: '/usage', label: 'Usage' },
    { href: '/audit', base: '/audit', label: 'Audit' },
    { href: '/settings/general', base: '/settings', label: 'Settings' },
  ];

  const sandbox = $derived(events.hello?.sandbox);
  const active = (base: string) => router.path === base || router.path.startsWith(base + '/');

  $effect(() => {
    if (router.path === '/') router.navigate('/chat', { replace: true });
  });
</script>

<div class="flex min-h-dvh bg-white text-zinc-900 dark:bg-zinc-950 dark:text-zinc-100">
  <nav aria-label="Main" class="hidden w-48 shrink-0 border-r border-zinc-200 p-3 sm:block dark:border-zinc-800">
    <a href="/chat" onclick={router.link} class="mb-4 block px-2 text-lg font-semibold">Bothy</a>
    <ul class="space-y-1">
      {#each nav as item (item.href)}
        <li>
          <a
            href={item.href}
            onclick={router.link}
            aria-current={active(item.base) ? 'page' : undefined}
            class="block rounded px-2 py-1 hover:bg-zinc-100 aria-[current=page]:bg-zinc-200 dark:hover:bg-zinc-900 dark:aria-[current=page]:bg-zinc-800"
          >
            {item.label}
          </a>
        </li>
      {/each}
    </ul>
  </nav>

  <div class="flex min-w-0 flex-1 flex-col">
    <header class="flex items-center gap-3 border-b border-zinc-200 px-4 py-2 dark:border-zinc-800">
      <span class="font-semibold sm:hidden">Bothy</span>
      <span
        class="rounded-full px-2 py-0.5 text-xs"
        class:bg-green-100={sandbox === true}
        class:text-green-900={sandbox === true}
        class:bg-red-100={sandbox === false}
        class:text-red-900={sandbox === false}
        class:bg-zinc-100={sandbox === undefined}
      >
        {sandbox === undefined ? 'Sandbox: unknown' : sandbox ? 'Isolated' : 'No sandbox: exec off'}
      </span>
    </header>

    <main class="flex-1 p-4">
      {#if router.current}
        <h1 class="text-xl font-semibold">{router.current.pattern}</h1>
        <p class="mt-2 text-sm text-zinc-500">This screen is not built yet.</p>
      {:else}
        <h1 class="text-xl font-semibold">Not found</h1>
        <p class="mt-2 text-sm"><a href="/chat" onclick={router.link} class="underline">Go to chat</a></p>
      {/if}
    </main>
  </div>
</div>
