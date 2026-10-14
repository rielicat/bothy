<script lang="ts">
  import { paths, type IconName } from '../lib/icons';

  let { name, size = 16, class: cls = '' }: { name: IconName; size?: number; class?: string } = $props();
</script>

<!-- non-scaling-stroke keeps the line at 1.5 CSS px whatever the drawn size (§3.7). -->
<svg
  viewBox="0 0 24 24"
  width={size}
  height={size}
  fill="none"
  stroke="currentColor"
  stroke-width="1.5"
  stroke-linecap="round"
  stroke-linejoin="round"
  aria-hidden="true"
  focusable="false"
  class="shrink-0 {cls}"><path d={paths[name]} vector-effect="non-scaling-stroke" /></svg>
