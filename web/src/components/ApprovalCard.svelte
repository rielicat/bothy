<script lang="ts">
  // The old import path, for screens not yet moved to components/approval.
  import type { Approval } from '../lib/api/types.gen';
  import { ApprovalCard } from './approval';

  let {
    approval,
    reason = '',
    preview = false,
    onresolved,
  }: { approval: Approval; reason?: string; compact?: boolean; shortcuts?: boolean; preview?: boolean; onresolved?: (a: Approval) => void } = $props();
</script>

<ApprovalCard {approval} {reason} {onresolved} mode={preview ? 'readonly' : 'inline'} />
