<script lang="ts">
  import type { Approval } from '../../lib/api/types.gen';
  import { ApprovalCard } from '../approval';

  // C4's card (§15.3 row 42) sends the decision itself and reads the viewer from the session.
  let { approval, readonly = false, decision = true, onresolved }: { approval: Approval; readonly?: boolean; decision?: boolean; onresolved?: (a?: Approval) => void } = $props();
</script>

<ApprovalCard {approval} mode={readonly ? 'readonly' : 'inline'} {decision} {onresolved} />
