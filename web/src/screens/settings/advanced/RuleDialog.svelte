<script lang="ts">
  import { Button, Dialog, FieldError, Select, TextField } from '../../../components/ui';
  import { config, saveErrorText } from '../../../lib/config.svelte';
  import { focusInvalid } from '../security/page.svelte';
  import { splitList, validRuleId, type Rule, type Verdict } from './rules';

  let {
    rule = $bindable(null),
    existing,
    ondelete,
  }: {
    /** The rule to edit, 'new' for a blank one, null when closed. */
    rule?: Rule | 'new' | null;
    existing: string[];
    ondelete: (r: Rule) => void;
  } = $props();

  const uid = $props.id();
  let form = $state({ id: '', tool: '', argv0: '', domains: '', paths: '', verdict: 'ask' as Verdict, reason: '' });
  let tried = $state(false);
  let busy = $state(false);
  let error = $state('');

  const original = $derived(rule && rule !== 'new' ? rule : null);
  const open = $derived(rule !== null);

  // Fill the form each time the dialog opens.
  let filledFor: Rule | 'new' | null = null;
  let initial = $state('');
  const dirty = $derived(open && JSON.stringify(form) !== initial);
  $effect(() => {
    if (rule === filledFor) return;
    filledFor = rule;
    if (!rule) return;
    const r = rule === 'new' ? null : rule;
    form = {
      id: r?.id ?? '',
      tool: r?.tool ?? '',
      argv0: r?.argv0.join(', ') ?? '',
      domains: r?.domains.join(', ') ?? '',
      paths: r?.paths.join(', ') ?? '',
      verdict: r?.verdict ?? 'ask',
      reason: r?.reason ?? '',
    };
    initial = JSON.stringify(form);
    tried = false;
    error = '';
  });

  const idError = $derived(
    !form.id.trim()
      ? 'Give the rule a name.'
      : !validRuleId(form.id.trim())
        ? 'Use lowercase letters, numbers and dashes.'
        : form.id.trim() !== original?.id && existing.includes(form.id.trim())
          ? 'Another rule has this name.'
          : '',
  );
  const toolError = $derived(form.tool.trim() ? '' : 'Name the action, for example shell or web_fetch.');

  async function save(e: SubmitEvent) {
    e.preventDefault();
    tried = true;
    if (idError || toolError) return focusInvalid(e.currentTarget);
    busy = true;
    const id = form.id.trim();
    const changes: Record<string, unknown> = {
      [`policy.rules.${id}`]: {
        id,
        tool: form.tool.trim(),
        argv0: splitList(form.argv0),
        domains: splitList(form.domains),
        paths: splitList(form.paths),
        verdict: form.verdict,
        reason: form.reason.trim(),
        match: original?.match ?? {},
        side_effect: original?.side_effect ?? '',
        triggers: original?.triggers ?? [],
      },
    };
    if (original && original.id !== id) changes[`policy.rules.${original.id}`] = null;
    const r = await config.save(changes);
    busy = false;
    if (!r.ok) {
      error = saveErrorText(r, undefined, { technical: true });
      return;
    }
    rule = null;
  }
</script>

<Dialog open={open} title={original ? `Edit rule ${original.id}` : 'Add rule'} size="md" {busy} {dirty} onclose={() => (rule = null)} cancel>
  <form id="{uid}-rule" class="space-y-5" onsubmit={save} novalidate>
    <div class="grid gap-5 sm:grid-cols-2">
      <TextField label="Name" placeholder="wiki-writes" autocomplete="off" spellcheck="false" bind:value={form.id} error={tried ? idError : ''} />
      <TextField label="Action" placeholder="shell" autocomplete="off" spellcheck="false" bind:value={form.tool} error={tried ? toolError : ''} help="A tool name; * matches any characters." />
    </div>
    <TextField label="First word of the command" optional placeholder="ls, cat, grep" autocomplete="off" spellcheck="false" bind:value={form.argv0} />
    <TextField label="Websites" optional placeholder="*.wikipedia.org" autocomplete="off" spellcheck="false" bind:value={form.domains} />
    <TextField label="Files" optional placeholder="notes/**" autocomplete="off" spellcheck="false" bind:value={form.paths} help="Separate several with commas." />
    <div class="grid gap-5 sm:grid-cols-2">
      <Select
        label="Then Bothy"
        bind:value={form.verdict}
        options={[
          { value: 'auto', label: 'Does it without asking' },
          { value: 'ask', label: 'Asks first' },
          { value: 'deny', label: 'Refuses' },
        ]}
      />
      <TextField label="Reason shown" optional placeholder="Reading the wiki is fine" bind:value={form.reason} />
    </div>
    <FieldError text={error} />
    {#if original}
      <!-- In the body, not the footer: on phones the footer becomes the sheet's Cancel / Save header. -->
      <Button variant="danger-text" size="sm" class="-ml-3" onclick={() => original && ondelete(original)}>Delete rule…</Button>
    {/if}
  </form>
  {#snippet footer()}
    <Button variant="primary" type="submit" form="{uid}-rule" loading={busy} loadingLabel="Saving…">Save rule</Button>
  {/snippet}
</Dialog>
