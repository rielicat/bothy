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

<!-- Dark on white in both themes, because phone cameras need that contrast; on-accent is the one token that stays white. -->
{#await path}
  <div class="size-44 shrink-0 rounded-md bg-subtle"></div>
{:then q}
  <svg viewBox="0 0 {q.n} {q.n}" class="size-44 shrink-0 rounded-md bg-on-accent" role="img" aria-label={label} shape-rendering="crispEdges">
    <path d={q.d} fill="#1e1d1a" />
  </svg>
{:catch}
  <p class="text-body text-muted">This link is too long for a QR code. Use the button instead.</p>
{/await}
