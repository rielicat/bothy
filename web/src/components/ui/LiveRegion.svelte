<script lang="ts">
  import { onMount } from 'svelte';
  import { live } from './live.svelte';

  const me = Symbol('live-region');
  onMount(() => {
    live.hosts.push(me);
    return () => (live.hosts = live.hosts.filter((h) => h !== me));
  });
  const primary = $derived(live.hosts[0] === me);
</script>

<!-- Mounted once by the shell; announce() fills it. -->
<div class="sr-only" aria-live="polite" aria-atomic="true">{primary ? live.polite : ''}</div>
<div class="sr-only" aria-live="assertive" aria-atomic="true">{primary ? live.assertive : ''}</div>
