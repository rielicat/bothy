<script lang="ts">
  import { approvalsIn } from '../../lib/cardcopy';
  import { duration } from '../../lib/format';
  import CodeBlock from '../ui/CodeBlock.svelte';
  import DiffView from '../ui/DiffView.svelte';
  import { people } from './people.svelte';
  import type { StepView } from './steps';

  // A step's Details (§7.4): the result and how it was allowed first, then
  // the exact request and the technical line. Technical words are fine here,
  // so the copy check skips it.
  let { step }: { step: StepView } = $props();

  const t = $derived(step.item);
  const action = $derived(step.input.action);
  const output = $derived(t.live?.out.map((o) => o.chunk).join('') ?? '');
  const result = $derived(output || step.input.result || '');
  const diff = $derived(t.tool === 'fs_write' ? (action?.diff ?? '') : '');
  // A diff shown as the result isn't printed a second time inside the request.
  const asked = $derived(JSON.stringify(action ? { ...action, fingerprint: undefined, ...(diff ? { diff: '(shown above)' } : {}) } : (t.args ?? {}), null, 2));
  const verdict = $derived(t.live?.verdict ?? step.record?.verdict ?? '');
  const rule = $derived(t.live?.rule ?? step.record?.rule ?? '');
  const ns = $derived(t.live?.ms != null ? t.live.ms * 1e6 : step.record?.duration);
  const untrusted = $derived(t.live?.untrusted ?? step.record?.untrusted ?? t.result?.untrusted);
  const source = $derived(/^web_/.test(t.tool) ? 'a web page' : /^(repo_|github_|knowledge)/.test(t.tool) ? 'a repository' : 'content Bothy read');

  const how = $derived.by(() => {
    const a = step.approval;
    if (a?.decided_by || (a && a.status !== 'pending' && a.status !== 'parked')) {
      const who = people.name(a.decided_by);
      if (a.status === 'allowed') return `${who} approved it`;
      if (a.status === 'denied') return `${who} denied it`;
      if (a.status === 'expired') return approvalsIn(a) ? 'Not enough approvals came in time' : 'Nobody answered in time';
      return 'The request was cancelled';
    }
    if (a) return 'Waiting for approval';
    if (verdict === 'deny') return "Bothy's rules don't allow this";
    if (rule.startsWith('grant:')) return 'Allowed by a saved permission';
    return verdict === 'auto' ? 'Allowed automatically' : '';
  });
</script>

<div class="mt-1 mb-3 space-y-3 pl-6 text-small text-text" data-details>
  {#if diff}
    <div>
      <p class="mb-1 text-muted">Result</p>
      <DiffView {diff} />
    </div>
  {:else if result}
    <div>
      <p class="mb-1 text-muted">Result</p>
      <CodeBlock code={result} wrap maxLines={20} />
    </div>
  {/if}
  {#if how}
    <div>
      <p class="text-muted">How it was allowed</p>
      <p>{how}</p>
    </div>
  {/if}
  <div>
    <p class="mb-1 text-muted">What Bothy asked for</p>
    <CodeBlock code={asked} wrap />
  </div>
  <div>
    <p class="text-muted">Technical</p>
    <!-- "How it was allowed" already says the decision in words. -->
    <p class="[overflow-wrap:anywhere]">
      Tool <code class="font-mono">{t.tool}</code>{#if verdict && !how}{' · '}Decision {verdict}{/if}{#if rule}{' · '}Rule <code class="font-mono">{rule}</code>{/if}{#if ns}{' · '}Took {duration(ns)}{/if}
    </p>
    {#if action?.host}<p>Contacted <code class="font-mono">{action.host}</code></p>{/if}
    {#if untrusted}<p>This came from outside your team ({source}). Bothy treats it with caution.</p>{/if}
  </div>
</div>
