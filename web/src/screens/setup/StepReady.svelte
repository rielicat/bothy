<script lang="ts">
  import { onMount } from 'svelte';
  import Icon from '../../components/Icon.svelte';
  import Button from '../../components/ui/Button.svelte';
  import { api, ApiError } from '../../lib/api/client';
  import { router } from '../../lib/router.svelte';
  import { session } from '../../lib/stores/session.svelte';
  import { NO_RESPONSE } from './auth';
  import Frame from './Frame.svelte';

  // Step 3 (docs/design/ui.md §5.7). Setup is complete as soon as this opens,
  // so every "Set up" goes to an ordinary app page.
  const rows = [
    { title: 'Connect a chat app', text: 'Your team can talk to Bothy in Slack, Discord or Telegram.', href: '/settings/connections' },
    { title: 'Connect GitHub', text: 'Let Bothy read your code and open pull requests for review.', href: '/settings/connections/github' },
    // C7's People page opens its "Add person" dialog for ?add=1.
    { title: 'Add people', text: 'Give admins and members their own sign-in to Bothy on the web.', href: '/settings/people?add=1' },
  ];

  const uid = $props.id();
  let done = $state(false);
  let error = $state('');

  async function complete(): Promise<boolean> {
    if (done) return true;
    try {
      await api.post('/api/setup/complete');
    } catch (e) {
      // 404: the setup routes are already gone, so setup is complete.
      if (!(e instanceof ApiError && e.status === 404)) return false;
    }
    done = true;
    session.finishing = true;
    if (session.setup) session.setup.needs_setup = false;
    return true;
  }

  onMount(() => {
    void complete();
    // Findings go to Security → Needs attention and its dot, never to this screen.
    void api.post('/api/security-audit', { fix: false }).catch(() => {});
  });

  async function leave(to: string) {
    error = '';
    if (!(await complete())) {
      error = NO_RESPONSE;
      return;
    }
    session.finishing = false;
    router.navigate(to);
  }
</script>

<Frame step={3} title="Bothy is ready" intro="Start a chat now, or connect more. Everything here is also in Settings.">
  <ul>
    {#each rows as r, i (r.href)}
      <li class="flex items-center gap-4 border-b border-border py-3 last:border-b-0 max-sm:flex-col max-sm:items-stretch max-sm:gap-3">
        <div class="min-w-0 flex-1">
          <p id="{uid}-{i}" class="text-body font-medium text-text">{r.title}</p>
          <p class="text-body text-muted">{r.text}</p>
        </div>
        <Button size="sm" class="max-sm:w-full" aria-describedby="{uid}-{i}" onclick={() => leave(r.href)}>Set up</Button>
      </li>
    {/each}
  </ul>
  {#if error}
    <p class="field-error mt-8" role="alert"><Icon name="alert" class="mt-px" /><span class="min-w-0">{error}</span></p>
  {/if}
  <Button variant="primary" class="{error ? 'mt-4' : 'mt-8'} h-10 max-sm:w-full pointer-coarse:h-11" onclick={() => leave('/chat')}>Start chatting</Button>
</Frame>
