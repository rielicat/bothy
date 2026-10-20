<script lang="ts">
  import { Button, CodeBlock, Dialog, Disclosure, Select, StatusLine, TextArea, TextField } from '../../../components/ui';
  import { api, errorText } from '../../../lib/api/client';
  import type { PolicyPreviewResp } from '../../../lib/api/types.gen';
  import { verdictSentence } from './rules';

  // Plans a sample action and says what Bothy would do; nothing runs (POST /api/policy/preview).
  let { open = $bindable(false) }: { open?: boolean } = $props();

  const uid = $props.id();
  let tool = $state('shell');
  let args = $state('{"command": "rm -rf build/"}');
  let trigger = $state('message');
  let busy = $state(false);
  let result = $state<PolicyPreviewResp | null>(null);
  let argsError = $state('');
  let failed = $state('');

  async function check(e: SubmitEvent) {
    e.preventDefault();
    result = null;
    failed = '';
    let parsed: unknown;
    try {
      parsed = JSON.parse(args.trim() || '{}');
    } catch {
      argsError = 'Write the details as a JSON object, like {"command": "ls"}.';
      return;
    }
    argsError = '';
    busy = true;
    try {
      result = await api.post<PolicyPreviewResp>('/api/policy/preview', { tool: tool.trim(), args: parsed, trigger });
    } catch (err) {
      failed = errorText(err);
    } finally {
      busy = false;
    }
  }
</script>

<Dialog bind:open title="Test a rule" size="md" {busy} onclose={() => ((result = null), (failed = ''))} cancel="Close">
  <form id="{uid}-test" class="space-y-5" onsubmit={check} novalidate>
    <div class="grid gap-5 sm:grid-cols-2">
      <TextField label="Action" autocomplete="off" spellcheck="false" bind:value={tool} />
      <Select
        label="Started from"
        bind:value={trigger}
        options={[
          { value: 'message', label: 'A chat message' },
          { value: 'schedule', label: 'A schedule' },
          { value: 'api', label: 'The API' },
        ]}
      />
    </div>
    <TextArea label="Details (JSON)" code rows={3} spellcheck="false" bind:value={args} error={argsError} />
  </form>
  {#if result}
    <div class="mt-5" aria-live="polite">
      <StatusLine tone={result.decision.verdict === 'deny' ? 'danger' : result.decision.verdict === 'ask' ? 'warning' : 'success'} text={verdictSentence(result.decision)} />
      <Disclosure class="mt-2 pl-6">
        <dl class="grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-1 text-small">
          <dt class="text-muted">Rule</dt>
          <dd class="font-mono text-code text-text">{result.decision.rule}</dd>
          <dt class="text-muted">Reason</dt>
          <dd class="text-text">{result.decision.reason || '—'}</dd>
        </dl>
        {#if result.action}<CodeBlock class="mt-2" label="Planned action" code={JSON.stringify(result.action, null, 2)} />{/if}
      </Disclosure>
    </div>
  {/if}
  {#if failed}<StatusLine class="mt-5" tone="danger" text="Bothy couldn't plan this action." details={failed} alert />{/if}
  {#snippet footer()}
    <Button variant="primary" type="submit" form="{uid}-test" loading={busy} loadingLabel="Checking…">Check</Button>
  {/snippet}
</Dialog>
