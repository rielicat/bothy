<script lang="ts">
  import { tick, untrack } from 'svelte';
  import { Button, Checkbox, CommandBlock, Dialog, Disclosure, Notice, OptionList, TextField, toast } from '../../../components/ui';
  import { api, ApiError, errorText } from '../../../lib/api/client';
  import type { MCPResolveResp, SandboxInfo } from '../../../lib/api/types.gen';
  import { config } from '../../../lib/config.svelte';
  import { session } from '../../../lib/stores/session.svelte';
  import { bothyKeyText, headerError, privateHost, saveConfig, type Failure } from './connections';
  import FailureLine from './FailureLine.svelte';
  import McpKey from './McpKey.svelte';

  let { open = $bindable(false), onadded }: { open?: boolean; onadded?: () => void } = $props();

  let name = $state('');
  let how = $state('http');
  let address = $state('');
  let allow = $state(false);
  let header = $state('');
  let ref = $state('');
  let key = $state('');
  let pkg = $state('');
  let version = $state('');
  // The exact version and integrity hash npm reports for pkg@version, which the server is pinned to.
  let pinned = $state<MCPResolveResp | null>(null);
  let pkgError = $state('');
  let hosts = $state('');
  let tried = $state(false);
  let busy = $state(false);
  let failure = $state<Failure | null>(null);
  let codeRuns = $state<boolean | undefined>(undefined);

  function reset() {
    name = address = header = ref = key = pkg = version = hosts = pkgError = '';
    pinned = null;
    how = 'http';
    tried = allow = false;
    failure = null;
    codeRuns = session.sandbox;
    api.get<SandboxInfo>('/api/sandbox').then((s) => (codeRuns = s.status.available), () => {});
  }

  $effect(() => {
    if (open) untrack(reset);
  });

  const taken = $derived(config.keys('mcp.servers'));
  // The name doubles as the server's key in the settings, so "My Docs" is kept as "my-docs" and shown back as "My docs".
  const id = $derived(
    name
      .trim()
      .toLowerCase()
      .replace(/[\s_]+/g, '-'),
  );
  const blocked = $derived(how === 'stdio' && codeRuns === false);
  // Opening a private address is a deliberate act (D15), so it is allowed here only when ticked.
  const priv = $derived(how === 'http' ? privateHost(address) : '');
  const errors = $derived({
    name: !id
      ? 'Give the server a name.'
      : id.length > 32
        ? 'Use 32 characters or fewer.'
        : !/^[a-z0-9][a-z0-9-]*$/.test(id)
          ? 'Use letters, numbers and spaces, like Linear.'
          : taken.includes(id)
            ? 'There is already a server with this name.'
            : undefined,
    address:
      how === 'http' && !/^https?:\/\/\S+$/.test(address.trim())
        ? 'Enter the full address, like https://mcp.linear.app/mcp.'
        : priv && !allow
          ? `Allow ${priv} below, or Bothy can't reach it.`
          : undefined,
    header: how === 'http' ? headerError(header) : undefined,
    pkg: how === 'stdio' && !pkg.trim() ? 'Enter the npm package, like @linear/mcp.' : how === 'stdio' && pkgError ? pkgError : undefined,
    version: how === 'stdio' && version.trim() && !/^\d+\.\d+\.\d+\S*$/.test(version.trim()) ? 'Enter an exact version, like 1.4.0, or leave it empty.' : undefined,
  });
  const show = (k: keyof typeof errors) => (tried ? errors[k] : undefined);
  const egress = $derived(
    hosts
      .split(',')
      .map((h) => h.trim())
      .filter(Boolean),
  );

  // pin asks the server for the exact version and hash; a package npm doesn't
  // know is a field error, anything else a failure under the form.
  async function pin(): Promise<MCPResolveResp | null> {
    const want = { name: pkg.trim(), version: version.trim() || undefined };
    if (pinned && pinned.name === want.name && (!want.version || pinned.version === want.version)) return pinned;
    pkgError = '';
    try {
      pinned = await api.post<MCPResolveResp>('/api/mcp/resolve', want);
      if (!pinned.command) [pinned, pkgError] = [null, 'This package has no program for Bothy to run.'];
      return pinned;
    } catch (err) {
      pinned = null;
      if (err instanceof ApiError && (err.status === 404 || err.status === 400)) pkgError = err.message;
      else failure = { message: "Bothy couldn't look this package up on npm. Try again.", details: errorText(err) };
      return null;
    }
  }

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
      const server: Record<string, unknown> = { name: id, transport: how, side_effects: { default: 'external_send' } };
      if (how === 'http') {
        let headers = {};
        const h = header.trim() || 'Authorization';
        if (ref) headers = { [h]: ref };
        else if (key.trim()) {
          await api.put(`/api/vault/global/mcp_${id}_header`, { value: key.trim() });
          headers = { [h]: `secret:mcp_${id}_header` };
        }
        Object.assign(server, { url: address.trim(), headers }, priv && { allow_private: true });
      } else {
        const p = await pin();
        if (!p) {
          await tick();
          form.querySelector<HTMLElement>('[aria-invalid="true"]')?.focus();
          return;
        }
        Object.assign(server, { package: { ecosystem: 'npm', name: p.name, version: p.version, integrity: p.integrity }, command: p.command, egress, workspace: 'none' });
      }
      failure = await saveConfig({ [`mcp.servers.${id}`]: server });
      if (failure) {
        failure.message = /use https/.test(failure.details ?? '')
          ? 'Use an https:// address: over http the secret header would be sent unencrypted.'
          : /belongs to Bothy/.test(failure.details ?? '')
            ? bothyKeyText
            : "Bothy couldn't add this server. Check the details and try again.";
        return;
      }
      if (how === 'stdio') await api.post(`/api/mcp/${encodeURIComponent(id)}/install`).catch(() => {});
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

<Dialog bind:open title="Add an MCP server" size="md" dirty={!!(name || address || key || pkg)} {busy} cancel>
  <form id="add-mcp" class="space-y-5" onsubmit={add} novalidate>
    <TextField label="Name" bind:value={name} error={show('name')} placeholder="Linear" autocomplete="off" spellcheck="false" />
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
              {#if priv}
                <Checkbox bind:checked={allow} label="Allow {priv}" description="It's on your private network. Bothy blocks private addresses unless you allow them." />
              {/if}
              <McpKey bind:header bind:ref bind:key error={{ header: show('header') }} />
            </div>
          {:else if blocked}
            <Notice tone="warning" text="This tool runs as a command, and running code is off on this server. Use its web address instead, or turn on running code in Security." />
          {:else}
            <div class="space-y-4">
              <TextField
                label="Package"
                bind:value={pkg}
                error={show('pkg') || pkgError || undefined}
                placeholder="@linear/mcp"
                spellcheck="false"
                help="From npm. Bothy installs it in an isolated workspace."
                oninput={() => ((pinned = null), (pkgError = ''))}
                onblur={() => pkg.trim() && void pin()}
              />
              <TextField
                label="Version"
                optional
                bind:value={version}
                error={show('version')}
                placeholder="Latest"
                spellcheck="false"
                help={pinned ? `Bothy will install version ${pinned.version} and check it hasn't changed.` : 'Leave it empty for the latest version.'}
                oninput={() => (pinned = null)}
                onblur={() => pkg.trim() && void pin()}
              />
              <TextField
                label="Websites it can reach"
                optional
                bind:value={hosts}
                placeholder="api.linear.app"
                spellcheck="false"
                help={egress.length ? `It will be able to reach ${egress.join(', ')}, and nothing else.` : "Leave this empty if it doesn't need the internet."}
              />
              {#if pinned}
                <Disclosure>
                  <CommandBlock label="Install command" code="npm install --ignore-scripts --no-audit --no-fund --save-exact {pinned.name}@{pinned.version}" />
                  <CommandBlock class="mt-3" label="Integrity hash" code={pinned.integrity} />
                </Disclosure>
              {/if}
            </div>
          {/if}
        {/snippet}
      </OptionList>
    </div>
    <FailureLine {failure} />
  </form>
  {#snippet footer()}
    <Button variant="primary" type="submit" form="add-mcp" disabled={blocked} loading={busy} loadingLabel="Adding…">Add server</Button>
  {/snippet}
</Dialog>
