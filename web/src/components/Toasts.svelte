<script lang="ts">
  import { toasts } from '../lib/stores/ui.svelte';
  import Icon from './Icon.svelte';
</script>

<div class="pointer-events-none fixed inset-x-0 bottom-4 z-50 flex flex-col items-center gap-2 px-4" role="status" aria-live="polite">
  {#each toasts.items as t (t.id)}
    <div
      class="pointer-events-auto flex max-w-md items-start gap-2 rounded-xl border border-line bg-surface px-4 py-3 text-sm shadow-lg {t.kind === 'error'
        ? 'text-bad'
        : ''}"
    >
      <Icon name={t.kind === 'error' ? 'alert' : t.kind === 'ok' ? 'check' : 'info'} size={16} class="mt-0.5 {t.kind === 'ok' ? 'text-ok' : ''}" />
      <p class="min-w-0 flex-1 text-fg">{t.text}</p>
      <button type="button" class="text-muted hover:text-fg" aria-label="Dismiss" onclick={() => toasts.dismiss(t.id)}><Icon name="x" size={15} /></button>
    </div>
  {/each}
</div>
