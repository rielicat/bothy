<script lang="ts" module>
  export function initials(name: string): string {
    const words = name.trim().split(/[\s._-]+/).filter(Boolean);
    if (!words.length) return '?';
    const first = [...words[0]][0] ?? '';
    const last = words.length > 1 ? ([...words[words.length - 1]][0] ?? '') : '';
    return (first + last).toLocaleUpperCase();
  }
</script>

<script lang="ts">
  // Decorative: the name is always shown in text next to it.
  let { name, size = 24, class: cls = '' }: { name: string; size?: 24 | 32; class?: string } = $props();
</script>

<span
  class="inline-flex shrink-0 items-center justify-center rounded-full bg-subtle font-semibold text-muted select-none {size === 24 ? 'size-6 text-caption' : 'size-8 text-small'} {cls}"
  aria-hidden="true">{initials(name)}</span
>
