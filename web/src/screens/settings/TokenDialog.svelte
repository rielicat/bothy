<script lang="ts">
  import Icon from '../../components/Icon.svelte';
  import { Button, Checkbox, CodeBlock, Dialog, Disclosure, Notice, Select, TextField } from '../../components/ui';
  import { api, refusal } from '../../lib/api/client';
  import type { TokenCreateResp, TokenRecord } from '../../lib/api/types.gen';
  import { session } from '../../lib/stores/session.svelte';
  import { scopeChoices } from './accountHelpers';
  import { focusDialogResult } from './dialogFocus';

  let { open = $bindable(false), oncreated }: { open?: boolean; oncreated?: (t: TokenRecord) => void } = $props();

  const formId = $props.id();
  const DAY = 86_400e9;
  const expiries = [
    { value: '7', label: 'In 7 days' },
    { value: '30', label: 'In 30 days' },
    { value: '90', label: 'In 90 days' },
    { value: '365', label: 'In 1 year' },
  ];

  let name = $state('');
  const noScopes = () => ({ chat: true, read: false, llm: false, admin: false });
  let scopes = $state<Record<string, boolean>>(noScopes());
  let days = $state('90');
  let submitted = $state(false);
  let busy = $state(false);
  let failed = $state('');
  let created = $state<string | null>(null);
  // /v1 takes only chat and llm tokens, so the instructions show for those alone.
  let forV1 = $state(false);

  const choices = $derived(scopeChoices(session.isAdmin));
  const chosen = $derived(choices.map((c) => c.value).filter((v) => scopes[v]));
  const nameError = $derived(submitted && !name.trim() ? 'Enter a name.' : undefined);
  const scopeError = $derived(submitted && !chosen.length ? 'Choose at least one.' : undefined);
  const origin = location.origin;

  $effect(() => {
    if (open) return;
    name = '';
    scopes = noScopes();
    days = '90';
    submitted = false;
    failed = '';
    created = null;
  });

  async function create(e: SubmitEvent) {
    e.preventDefault();
    submitted = true;
    failed = '';
    if (nameError || scopeError) return;
    busy = true;
    try {
      // Tokens never approve anything; requests that need approval are denied.
      const r = await api.post<TokenCreateResp>('/api/tokens', { name: name.trim(), scopes: chosen, ttl: Number(days) * DAY, on_approval: 'deny', rate_per_min: 30 });
      created = r.token;
      forV1 = chosen.includes('chat') || chosen.includes('llm');
      if (r.meta) oncreated?.(r.meta);
      void focusDialogResult();
    } catch (err) {
      failed = refusal(err) || "Bothy couldn't create the token. Try again.";
    } finally {
      busy = false;
    }
  }

  const curl = (tok: string) =>
    `curl ${origin}/v1/chat/completions \\\n  -H "Authorization: Bearer ${tok}" \\\n  -H "Content-Type: application/json" \\\n  -d '{"model":"bothy","messages":[{"role":"user","content":"Hello"}]}'`;
</script>

<Dialog bind:open title={created ? 'Copy your token' : 'Create token'} size="md" {busy} dirty={!created && !!name} cancel={!created} lockCancel>
  {#if created}
    <p class="text-body text-text">Copy this token now. It won't be shown again.</p>
    <CodeBlock class="mt-3" code={created} wrap />
    {#if forV1}<Disclosure class="mt-4">
      <div class="grid gap-3 text-small text-muted">
        <p>Bothy answers on an OpenAI-compatible address, {origin}/v1, with the model “bothy”. Send the token as a bearer token.</p>
        <CodeBlock label="curl" code={curl(created)} />
        <p>Open WebUI: Settings → Connections → OpenAI API. Use {origin}/v1 as the URL, the token as the key and “bothy” as the model.</p>
      </div>
    </Disclosure>{/if}
  {:else}
    <form id={formId} class="grid gap-5" onsubmit={create} novalidate>
      <TextField label="Name" placeholder="Open WebUI on the laptop" bind:value={name} error={nameError} maxlength={64} />
      <fieldset aria-describedby={scopeError ? `${formId}-scopes` : undefined}>
        <legend class="field-label">What it can do</legend>
        <div class="mt-1">
          {#each choices as c (c.value)}
            <Checkbox label={c.label} description={c.hint} bind:checked={scopes[c.value]} />
          {/each}
        </div>
        {#if scopeError}<p id="{formId}-scopes" class="field-error"><Icon name="alert" class="mt-px" /><span>{scopeError}</span></p>{/if}
      </fieldset>
      <Select label="Expires" bind:value={days} options={expiries} />
      {#if failed}<Notice tone="danger" text={failed} role="alert" />{/if}
    </form>
  {/if}
  {#snippet footer({ close })}
    {#if created}
      <Button variant="primary" onclick={close}>Done</Button>
    {:else}
      <Button variant="primary" type="submit" form={formId} loading={busy} loadingLabel="Creating…">Create token</Button>
    {/if}
  {/snippet}
</Dialog>
