<script lang="ts">
  let { text, label }: { text: string; label: string } = $props();

  // The encoder is its own chunk: only pairing screens need it.
  const path = $derived(
    import('../lib/qr').then(({ qr }) => {
      const g = qr(text);
      let d = '';
      g.forEach((row, y) => row.forEach((dark, x) => dark && (d += `M${x + 4} ${y + 4}h1v1h-1z`)));
      return { d, n: g.length + 8 };
    }),
  );
</script>

{#await path}
  <div class="skeleton size-44 rounded-xl"></div>
{:then q}
  <svg viewBox="0 0 {q.n} {q.n}" class="size-44 rounded-xl bg-white p-1 shadow-sm" role="img" aria-label={label} shape-rendering="crispEdges">
    <path d={q.d} fill="#111" />
  </svg>
{:catch}
  <p class="text-sm text-muted">This link is too long for a QR code; use the button instead.</p>
{/await}
