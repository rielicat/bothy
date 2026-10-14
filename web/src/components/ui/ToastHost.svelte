<script lang="ts">
  import { onMount } from 'svelte';
  import { toaster } from './toast.svelte';

  // 'main' leaves room for the 256 px sidebar on desktop; 'page' is for screens without one (setup, sign in).
  let { inset = 'main' }: { inset?: 'main' | 'page' } = $props();

  const me = Symbol('toast-host');
  onMount(() => {
    toaster.hosts.push(me);
    return () => (toaster.hosts = toaster.hosts.filter((h) => h !== me));
  });
  const t = $derived(toaster.hosts[0] === me ? toaster.current : null);
</script>

<!-- The status region stays mounted so screen readers notice each new toast. -->
<div
  class="pointer-events-none fixed bottom-4 left-4 z-50 max-sm:right-4 {inset === 'main' ? 'lg:left-68' : ''}"
  style:margin-bottom={toaster.offset ? `${toaster.offset}px` : undefined}
  role="status"
>
  {#if t}
    {#key t.id}
      <!-- Hover and focus only pause the timer. -->
      <!-- svelte-ignore a11y_no_static_element_interactions -->
      <div
        data-floating
        class="pointer-events-auto inline-flex max-w-md items-center gap-3 rounded-lg bg-text px-4 py-2.5 text-body text-bg shadow-[var(--shadow-overlay)] transition-[opacity,translate] duration-160 ease-out starting:translate-y-2 starting:opacity-0 max-sm:text-small"
        onpointerenter={() => toaster.pause()}
        onpointerleave={() => toaster.resume()}
        onfocusin={() => toaster.pause()}
        onfocusout={() => toaster.resume()}
      >
        <span class="min-w-0">{t.text}</span>
        {#if t.action}
          {@const action = t.action}
          <button
            type="button"
            class="-my-1 -mr-2 inline-flex min-h-7 shrink-0 cursor-pointer items-center rounded-md px-2 font-medium text-bg underline decoration-bg/40 underline-offset-2 hover:bg-bg/10 hover:decoration-bg focus-visible:outline-bg pointer-coarse:-my-3 pointer-coarse:min-h-11"
            onclick={() => {
              action.onclick();
              toaster.dismiss(t.id);
            }}>{action.label}</button
          >
        {/if}
      </div>
    {/key}
  {/if}
</div>
