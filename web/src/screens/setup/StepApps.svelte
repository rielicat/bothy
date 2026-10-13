<script lang="ts">
  import ChannelSetup from '../../components/ChannelSetup.svelte';
  import Icon from '../../components/Icon.svelte';
  import { originIcon } from '../../lib/labels';

  let { onnext }: { onnext: () => void } = $props();

  let open = $state<'' | 'telegram' | 'discord' | 'slack'>('');
  const apps = [
    { id: 'telegram', name: 'Telegram', text: 'A private bot in your pocket. Takes two minutes.' },
    { id: 'discord', name: 'Discord', text: 'Talk to Bothy in DMs or in your server.' },
    { id: 'slack', name: 'Slack', text: 'For a workspace; uses Socket Mode, no public URL needed.' },
  ] as const;
</script>

<h1 class="text-2xl font-semibold tracking-tight">Chat from your phone</h1>
<p class="mt-2 text-muted">Optional. Connect a chat app to talk to Bothy and approve actions on the go. Link previews are always off.</p>

<div class="mt-6 space-y-3">
  {#each apps as a (a.id)}
    <div class="rounded-xl border {open === a.id ? 'border-accent' : 'border-line'}">
      <button type="button" class="flex w-full items-center gap-3 p-4 text-left" aria-expanded={open === a.id} onclick={() => (open = open === a.id ? '' : a.id)}>
        <span class="grid size-10 place-items-center rounded-xl bg-sunken"><Icon name={originIcon[a.id]} size={20} /></span>
        <span class="min-w-0 flex-1"><span class="font-medium">{a.name}</span><span class="block text-sm text-muted">{a.text}</span></span>
        <Icon name="chevron" size={16} class="text-faint transition-transform {open === a.id ? 'rotate-180' : ''}" />
      </button>
      {#if open === a.id}<div class="border-t border-line p-4"><ChannelSetup name={a.id} /></div>{/if}
    </div>
  {/each}
</div>

<div class="mt-8 flex justify-end gap-2">
  <button type="button" class="btn btn-ghost" onclick={onnext}>Skip for now</button>
  <button type="button" class="btn btn-primary" onclick={onnext}>Continue</button>
</div>
