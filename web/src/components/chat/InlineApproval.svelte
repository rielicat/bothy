<script lang="ts">
  import type { Component } from 'svelte';
  import type { Approval } from '../../lib/api/types.gen';
  import { session } from '../../lib/stores/session.svelte';
  import { ApprovalCard } from '../approval';

  // C4 rebuilds the card behind this import with the §15.3 row 42 props
  // (mode, viewer, onresolve); until then it is the v0.1 card (compact,
  // preview, onresolved). Both are passed, so either one renders here.
  const Card = ApprovalCard as unknown as Component<Record<string, unknown>>;

  let { approval, readonly = false, onresolved }: { approval: Approval; readonly?: boolean; onresolved?: (a?: Approval) => void } = $props();

  const viewer = $derived(session.me ? { user_id: session.me.user.id, admin: session.isAdmin, principals: session.principals.map((p) => p.id) } : null);
</script>

<Card {approval} mode={readonly ? 'readonly' : 'inline'} {viewer} compact preview={readonly} {onresolved} onresolve={() => onresolved?.()} />
