<script lang="ts">
  // Placeholder for /knowledge/projects/:owner/:repo until C5 builds it (docs/design/ui.md §9.2).
  let { params }: { params: Record<string, string> } = $props();
  const name = $derived(`${params.owner}/${params.repo}`);
</script>

<svelte:head><title>{name} · Bothy</title></svelte:head>

<div class="page">
  <h1 class="page-title" tabindex="-1">{name}</h1>
  <p class="mt-6 text-reading text-muted">This page isn't ready yet.</p>
</div>
