<script lang="ts">
  import { tick, untrack } from 'svelte';
  import { Button, CommandBlock, Dialog, Disclosure, Notice, OptionList, StatusLine, TextField, toast } from '../../../components/ui';
  import { api, errorText } from '../../../lib/api/client';
  import type { SandboxInfo } from '../../../lib/api/types.gen';
  import { config } from '../../../lib/config.svelte';
  import { session } from '../../../lib/stores/session.svelte';
  import { saveConfig, type Failure } from './connections';

  let { open = $bindable(false), onadded }: { open?: boolean; onadded?: () => void } = $props();

  let name = $state('');
  let how = $state('http');
  let address = $state('');
  let header = $state('');
  let pkg = $state('');
  let version = $state('');
  let integrity = $state('');
  let hosts = $state('');
  let tried = $state(false);
  let busy = $state(false);
  let failure = $state<Failure | null>(null);
  let codeRuns = $state<boolean | undefined>(undefined);

  function reset() {
    name = address = header = pkg = version = integrity = hosts = '';
    how = 'http';
    tried = false;
    failure = null;
    codeRuns = session.sandbox;
    api.get<SandboxInfo>('/api/sandbox').then((s) => (codeRuns = s.status.available), () => {});
  }

  $effect(() => {
    if (open) untrack(reset);
  });

  const taken = $derived(config.keys('mcp.servers'));
  const blocked = $derived(how === 'stdio' && codeRuns === false);
  const errors = $derived({
    name: !name
      ? 'Give the server a name.'
      : !/^[a-z0-9][a-z0-9-]{0,31}$/.test(name)
        ? 'Use lowercase letters, numbers and -, like linear.'
        : taken.includes(name)
          ? 'There is already a server with this name.'
          : undefined,
    address: how === 'http' && !/^https?:\/\/\S+$/.test(address.trim()) ? 'Enter the full address, like https://mcp.linear.app/mcp.' : undefined,
    pkg: how === 'stdio' && !pkg.trim() ? 'Enter the npm package, like @linear/mcp.' : undefined,
    version: how === 'stdio' && !/^\d+\.\d+\.\d+\S*$/.test(version.trim()) ? 'Enter an exact version, like 1.4.0.' : undefined,
    integrity: how === 'stdio' && !/^sha(256|384|512)-\S+$/.test(integrity.trim()) ? 'Paste the integrity hash from npm, starting with sha512-.' : undefined,
  });
  const show = (k: keyof typeof errors) => (tried ? errors[k] : undefined);
  const egress = $derived(
    hosts
      .split(',')
      .map((h) => h.trim())
      .filter(Boolean),
  );

  async function add(e: SubmitEvent) {
    e.preventDefault();
    const form = e.currentTarget as HTMLFormElement;
    tried = true;
    if (blocked) return;
    if (Object.values(errors).some(Boolean)) {
      await tick();
      form.querySelector<HTMLElement>('[aria-invalid="true"]')?.focus();
      return;
    }
    busy = true;
    failure = null;
    try {
      const server: Record<string, unknown> = { name, transport: how, side_effects: { default: 'external_send' } };
      if (how === 'http') {
        let headers = {};
        if (header.trim()) {
          await api.put(`/api/vault/global/mcp_${name}_header`, { value: header.trim() });
          headers = { Authorization: `secret:mcp_${name}_header` };
        }
        Object.assign(server, { url: address.trim(), headers });
      } else {
        Object.assign(server, { package: { ecosystem: 'npm', name: pkg.trim(), version: version.trim(), integrity: integrity.trim() }, egress, workspace: 'none' });
      }
      failure = await saveConfig({ [`mcp.servers.${name}`]: server });
      if (failure) {
        failure.message = "Bothy couldn't add this server. Check the details and try again.";
        return;
      }
      if (how === 'stdio') await api.post(`/api/mcp/${encodeURIComponent(name)}/install`).catch(() => {});
      open = false;
      toast('Server added.');
      onadded?.();
    } catch (err) {
      failure = { message: "Bothy couldn't add this server. Try again.", details: errorText(err) };
    } finally {
      busy = false;
    }
  }
</script>

<Dialog bind:open title="Add an MCP server" size="md" dirty={!!(name || address || pkg)} {busy}>
  <form id="add-mcp" class="space-y-5" onsubmit={add} novalidate>
    <TextField label="Name" bind:value={name} error={show('name')} placeholder="linear" autocomplete="off" spellcheck="false" />
    <div>
      <p class="field-label">How does it run?</p>
      <OptionList
        class="mt-1"
        name="mcp-how"
        label="How does it run?"
        framed={false}
        bind:value={how}
        options={[
          { value: 'http', title: 'A web address (recommended)' },
          { value: 'stdio', title: 'A command on this server' },
        ]}
      >
        {#snippet expanded(v)}
          {#if v === 'http'}
            <div class="space-y-4">
              <TextField label="Address" type="url" bind:value={address} error={show('address')} placeholder="https://mcp.linear.app/mcp" spellcheck="false" />
              <TextField
                label="Secret header"
                optional
                type="password"
                bind:value={header}
                autocomplete="off"
                help="Sent as the Authorization header, such as Bearer abc123. Stored encrypted and only sent to this server."
              />
            </div>
          {:else if blocked}
            <Notice tone="warning" text="This server runs as a command, and code can't run on this server. Use its web address instead, or turn on running code in Security." />
          {:else}
            <div class="space-y-4">
              <TextField label="Package" bind:value={pkg} error={show('pkg')} placeholder="@linear/mcp" spellcheck="false" help="From npm. Bothy installs it in an isolated workspace." />
              <div class="grid gap-4 sm:grid-cols-2">
                <TextField label="Version" bind:value={version} error={show('version')} placeholder="1.4.0" spellcheck="false" />
                <TextField label="Integrity hash" bind:value={integrity} error={show('integrity')} placeholder="sha512-…" spellcheck="false" />
              </div>
              <TextField
                label="Websites it can reach"
                optional
                bind:value={hosts}
                placeholder="api.linear.app"
                spellcheck="false"
                help={egress.length ? `It will be able to reach ${egress.join(', ')}, and nothing else.` : "Leave this empty if it doesn't need the internet."}
              />
              {#if pkg.trim() && version.trim()}
                <Disclosure>
                  <CommandBlock label="Install command" code="npm install --ignore-scripts --no-audit --no-fund --save-exact {pkg.trim()}@{version.trim()}" />
                </Disclosure>
              {/if}
            </div>
          {/if}
        {/snippet}
      </OptionList>
    </div>
    {#if failure}<StatusLine tone="danger" alert text={failure.message} details={failure.details} />{/if}
  </form>
  {#snippet footer({ close })}
    <Button data-cancel onclick={close}>Cancel</Button>
    <Button variant="primary" type="submit" form="add-mcp" loading={busy} loadingLabel="Adding…">Add server</Button>
  {/snippet}
</Dialog>
