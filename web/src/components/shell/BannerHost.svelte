<script lang="ts">
  import { api } from '../../lib/api/client';
  import { router } from '../../lib/router.svelte';
  import { session } from '../../lib/stores/session.svelte';
  import { shell } from '../../lib/stores/ui.svelte';
  import Banner from '../ui/Banner.svelte';
  import { toast } from '../ui/toast.svelte';
  import { chooseBanner, type BannerAction } from './banners';

  let { hide = [] }: { hide?: string[] } = $props();

  const b = $derived(chooseBanner({ banners: session.banners, paused: session.paused, admin: session.isAdmin, today: session.today, dismissed: session.dismissed, hide, pause: session.pause }));
  let failed = $state('');

  async function resume() {
    failed = '';
    try {
      await api.post('/api/admin/pause', { paused: false });
      session.paused = false;
      session.banners = session.banners.filter((x) => x.code !== 'paused');
      toast('Bothy resumed.');
    } catch {
      failed = "Couldn't resume Bothy. Try again.";
    }
  }

  function act(a: BannerAction) {
    if ('href' in a) router.navigate(a.href);
    else if (a.run === 'resume') void resume();
    else shell.unlock = true;
  }
</script>

{#if b}
  {#key b.code}
    <Banner
      tone={b.tone}
      text={b.code === 'paused' && failed ? failed : b.text}
      action={b.action && { label: b.action.label, onclick: () => act(b.action!) }}
      dismissible={b.dismissible}
      ondismiss={() => session.dismiss(b.code)}
    />
  {/key}
{/if}
