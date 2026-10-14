<script lang="ts">
  // "Loading…" only after 400 ms (§13.2), so a quick load shows nothing.
  let slow = $state(false);
  $effect(() => {
    const t = setTimeout(() => (slow = true), 400);
    return () => clearTimeout(t);
  });
</script>

<p class="text-body text-muted" class:invisible={!slow}>Loading…</p>
