<script lang="ts">
  import type { IconName } from '../lib/icons';

  let { name, size = 16, class: cls = '' }: { name: IconName; size?: number; class?: string } = $props();
</script>

<!-- The shapes live in index.html's sprite (§3.7); stroke settings inherit through <use>. -->
<svg
  width={size}
  height={size}
  fill="none"
  stroke="currentColor"
  stroke-width="1.5"
  stroke-linecap="round"
  stroke-linejoin="round"
  aria-hidden="true"
  focusable="false"
  class="shrink-0 {cls}"><use href="#i-{name}" /></svg>
