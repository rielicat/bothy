<script lang="ts">
  import type { Approval } from '../../lib/api/types.gen';
  import { date, duration } from '../../lib/format';
  import Icon from '../Icon.svelte';
  import Spinner from '../ui/Spinner.svelte';
  import Verbatim from '../Verbatim.svelte';
  import { deploys } from './deploys.svelte';

  // What became of an approved deploy (§8.7), from the deploy list every line
  // on screen shares.
  let { approval, small = false }: { approval: Approval; /** Sized to sit under a decided line in a list. */ small?: boolean } = $props();

  $effect(() => deploys.watch(approval.id));
  const deploy = $derived(deploys.of(approval.id));

  const took = $derived.by(() => {
    const a = date(deploy?.created_at);
    const b = date(deploy?.finished_at);
    return a && b && b > a ? duration((b.getTime() - a.getTime()) * 1e6) : '';
  });
</script>

{#snippet github(label: string)}
  {#if deploy?.run_url}{' · '}<a class="link" href={deploy.run_url} target="_blank" rel="noopener noreferrer">{label}<span aria-hidden="true">{' ↗'}</span><span class="sr-only">{' (opens in a new tab)'}</span></a>{/if}
{/snippet}

{#if deploy}
  <p class="flex items-start text-muted {small ? 'gap-1.5 text-small' : 'gap-2 text-body'}" role="status">
    <span class="flex w-4 shrink-0 items-center justify-center {small ? 'h-[1.125rem]' : 'h-5'}">
      {#if deploy.status === 'succeeded'}
        <Icon name="check" class="text-accent-text" />
      {:else if deploy.status === 'failed'}
        <Icon name="alert" class="text-danger-text" />
      {:else if deploy.status === 'unknown'}
        <Icon name="alert" />
      {:else}
        <Spinner size={16} />
      {/if}
    </span>
    <span class="min-w-0 {deploy.status === 'failed' ? 'text-danger-text' : ''}">
      {#if deploy.status === 'succeeded'}
        Deployed <span class="font-mono text-code" data-copy-ignore><Verbatim text={deploy.repo} /></span> to {deploy.environment}{took ? ` in ${took}` : ''}
      {:else if deploy.status === 'failed'}
        Deploy to {deploy.environment} failed{#if deploy.failed_step}{' at '}<span data-copy-ignore>“<Verbatim text={deploy.failed_step} />”</span>{/if}{@render github('View logs on GitHub')}
      {:else if deploy.status === 'unknown'}
        Bothy stopped tracking the deploy of <span class="font-mono text-code" data-copy-ignore><Verbatim text={deploy.repo} /></span> to {deploy.environment}{@render github('Check on GitHub')}
      {:else}
        Deploying <span class="font-mono text-code" data-copy-ignore><Verbatim text={deploy.repo} /></span> to {deploy.environment}…{@render github('View on GitHub')}
      {/if}
    </span>
  </p>
{/if}
