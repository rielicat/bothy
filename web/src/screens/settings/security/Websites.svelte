<script lang="ts">
  import { tick, untrack } from 'svelte';
  import Icon from '../../../components/Icon.svelte';
  import { Button, Dialog, EmptyState, Notice, Page, SettingsSection, TextField, toast } from '../../../components/ui';
  import { api } from '../../../lib/api/client';
  import type { EgressRecord, List, Session } from '../../../lib/api/types.gen';
  import { config, saveErrorText } from '../../../lib/config.svelte';
  import { fullTime, relTime } from '../../../lib/format';
  import { session } from '../../../lib/stores/session.svelte';
  import { focusInvalid, relLower, securityBack } from './page.svelte';
  import PageState from './PageState.svelte';
  import { recentBlocks, sharedSuffix, siteEntry, siteOf } from './sites';

  const PATH = 'egress.allow';
  const uid = $props.id();

  let records = $state<EgressRecord[]>([]);
  let titles = $state<Record<string, string>>({});
  let busy = $state('');
  let rowError = $state<{ site: string; text: string } | null>(null);
  let list: HTMLElement | undefined = $state();

  let adding = $state(false);
  let typed = $state('');
  let addError = $state('');
  let tried = $state(false);
  let saving = $state(false);
  let allowing = $state<EgressRecord | null>(null);
  let allowError = $state('');

  const allow = $derived((config.value<string[]>(PATH, []) ?? []).map(String));
  const entry = $derived(config.entry(PATH));
  const readOnly = $derived(!!entry && (entry.locked || entry.source.layer === 'env'));
  const blocked = $derived(recentBlocks(records, allow));
  const suffixes = $derived(config.value<string[]>('egress.shared_tenant_suffixes', []) ?? []);
  const allowSite = $derived(allowing ? siteOf(allowing.host, allowing.port) : '');
  const allowShared = $derived(allowSite ? sharedSuffix(allowSite, suffixes) : undefined);

  function load() {
    void config.load(true);
    api.get<List<EgressRecord>>('/api/egress?limit=200').then(
      (l) => (records = l.items ?? []),
      () => {},
    );
    api.get<List<Session>>('/api/sessions?limit=200').then(
      (l) => (titles = Object.fromEntries((l.items ?? []).filter((s) => s.title).map((s) => [s.id, s.title]))),
      () => {},
    );
  }

  $effect(() => {
    if (session.isAdmin) untrack(load);
  });

  async function write(next: string[]): Promise<string> {
    const r = await config.save({ [PATH]: next });
    return r.ok ? '' : saveErrorText(r, PATH);
  }

  async function remove(site: string, i: number) {
    busy = site;
    rowError = null;
    const err = await write(allow.filter((a) => a !== site));
    busy = '';
    if (err) {
      rowError = { site, text: err };
      return;
    }
    toast('Website removed.');
    await tick();
    const buttons = list?.querySelectorAll<HTMLElement>('[data-remove]') ?? [];
    (buttons[Math.min(i, buttons.length - 1)] ?? document.querySelector<HTMLElement>('[data-add-site]'))?.focus();
  }

  const typedError = $derived.by(() => {
    if (!typed.trim()) return 'Enter a website address, like docs.example.com.';
    const s = siteEntry(typed);
    if (!s) return 'Enter a website address, like docs.example.com.';
    if (allow.includes(s)) return `Bothy can already reach ${s}.`;
    return '';
  });

  async function add(e: SubmitEvent) {
    e.preventDefault();
    tried = true;
    addError = typedError;
    if (addError) return focusInvalid(e.currentTarget);
    saving = true;
    const site = siteEntry(typed)!;
    addError = await write([...allow, site]);
    saving = false;
    if (addError) return;
    adding = false;
    toast('Website added.');
  }

  async function allowBlocked() {
    if (!allowing) return;
    saving = true;
    allowError = await write([...allow, allowSite]);
    saving = false;
    if (allowError) return;
    allowing = null;
    toast('Website allowed.');
  }
</script>

<Page title="Websites Bothy can reach" back={securityBack}>
  {#snippet actions()}
    {#if session.isAdmin && config.view && !readOnly}
      <Button data-add-site onclick={() => (adding = true)}>Add website</Button>
    {/if}
  {/snippet}
  {#if !session.isAdmin}
    <PageState kind="forbidden" />
  {:else if !config.view && config.error}
    <PageState kind="failed" onretry={load} />
  {:else if !config.view}
    <PageState kind="loading" />
  {:else}
    {#if readOnly}<Notice class="mb-4" text="This list is set on the server and can't be changed here." />{/if}
    {#if !allow.length}
      <EmptyState text="No websites yet." more="Code Bothy runs can only reach the websites you add here." />
    {:else}
      <div bind:this={list}>
        {#each allow as site, i (site)}
          <div class="border-b border-border last:border-b-0">
            <div class="flex min-h-10 items-center gap-3 py-2 pointer-coarse:min-h-12">
              <span class="min-w-0 flex-1 font-mono text-code text-text [overflow-wrap:anywhere]" data-copy-ignore>{site}</span>
              {#if !readOnly}
                <Button variant="ghost" size="sm" data-remove aria-label="Remove {site}" loading={busy === site} loadingLabel="Removing…" onclick={() => remove(site, i)}>Remove</Button>
              {/if}
            </div>
            {#if rowError?.site === site}<p class="field-error mb-2" role="alert"><Icon name="alert" class="mt-px" />{rowError.text}</p>{/if}
          </div>
        {/each}
      </div>
    {/if}

    <SettingsSection title="Blocked recently">
      {#if !blocked.length}
        <p class="py-3 text-body text-muted">Nothing was blocked recently.</p>
      {:else}
        {#each blocked as e (e.id)}
          {@const site = siteOf(e.host, e.port)}
          <div class="flex min-h-14 items-center gap-3 border-b border-border py-2 last:border-b-0 pointer-coarse:min-h-12">
            <div class="min-w-0 flex-1">
              <p id="{uid}-blocked-{e.id}" class="font-mono text-code text-text [overflow-wrap:anywhere]" data-copy-ignore>{site}</p>
              <p class="text-small text-muted">
                {#if e.session_id && titles[e.session_id]}From “{titles[e.session_id]}”{' · '}{/if}<time datetime={e.at} title={fullTime(e.at)}>{e.session_id && titles[e.session_id] ? relLower(e.at) : relTime(e.at)}</time>
              </p>
            </div>
            {#if !readOnly}
              <Button size="sm" aria-describedby="{uid}-blocked-{e.id}" onclick={() => ((allowing = e), (allowError = ''))}>Allow this site…</Button>
            {/if}
          </div>
        {/each}
      {/if}
    </SettingsSection>
  {/if}
</Page>

<Dialog bind:open={adding} title="Add website" size="md" busy={saving} dirty={!!typed} onclose={() => ((typed = ''), (addError = ''), (tried = false))}>
  <form id="{uid}-add" onsubmit={add} novalidate>
    <TextField
      label="Website address"
      placeholder="docs.example.com"
      autocomplete="off"
      autocapitalize="off"
      spellcheck="false"
      bind:value={typed}
      help="Add *. in front to include every subdomain, like *.example.com."
      error={tried ? typedError || addError : addError}
    />
  </form>
  {#snippet footer({ close })}
    <Button data-cancel onclick={close}>Cancel</Button>
    <Button variant="primary" type="submit" form="{uid}-add" loading={saving} loadingLabel="Adding…">Add website</Button>
  {/snippet}
</Dialog>

<Dialog open={!!allowing} title="Allow {allowSite}?" busy={saving} onclose={() => (allowing = null)}>
  <p class="text-body text-text">Code Bothy runs will be able to reach {allowSite} in every chat. You can remove it from this list at any time.</p>
  {#if allowShared}
    <Notice tone="warning" class="mt-3" text="Anyone can publish on {allowShared.replace(/^\./, '')}, so this also lets Bothy reach other people's sites there." />
  {/if}
  {#if allowError}<p class="field-error" role="alert"><Icon name="alert" class="mt-px" />{allowError}</p>{/if}
  {#snippet footer({ close })}
    <Button data-cancel onclick={close}>Cancel</Button>
    <Button variant="primary" loading={saving} loadingLabel="Allowing…" onclick={allowBlocked}>Allow site</Button>
  {/snippet}
</Dialog>
