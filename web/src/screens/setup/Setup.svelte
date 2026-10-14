<script lang="ts">
  import { onMount, tick } from 'svelte';
  import { api, ApiError, setCsrf } from '../../lib/api/client';
  import type { Effective, SetupClaimResp } from '../../lib/api/types.gen';
  import { router } from '../../lib/router.svelte';
  import { session } from '../../lib/stores/session.svelte';
  import { codeError, type SetupStateV2 } from './auth';
  import SetupCode from './SetupCode.svelte';
  import StepAccount, { type Account } from './StepAccount.svelte';
  import StepModel from './StepModel.svelte';
  import StepReady from './StepReady.svelte';

  // Setup (docs/design/ui.md §5.1). State comes from the server, so a reload
  // resumes at the right step: no owner yet → the code or the account; an
  // owner → the model step until a main model is set, then the last step.
  type Screen = 'wait' | 'code' | 'account' | 'model' | 'ready';

  const setupState = session.setup as SetupStateV2 | null;
  const token = /^#t=([^&]+)/.exec(location.hash)?.[1];

  function initial(): Screen {
    if (session.finishing) return 'ready';
    if (setupState?.owner_set) return 'wait';
    // After a reload the setup state hands back the claimed grant's token.
    if (setupState?.claimed && setupState.csrf) {
      setCsrf(setupState.csrf);
      return 'account';
    }
    return token ? 'wait' : 'code';
  }

  let screen = $state<Screen>(initial());
  let codeErr = $state('');
  let notice = $state('');
  let account = $state<Account>({ name: '', username: '', password: '' });

  async function go(next: Screen) {
    screen = next;
    window.scrollTo({ top: 0 });
    await tick();
    // The old screen took focus with it; don't steal it if the person already moved on.
    if (document.activeElement === document.body) document.querySelector<HTMLElement>('main h1')?.focus();
  }

  async function claim(code: string): Promise<string> {
    try {
      const { csrf } = await api.post<SetupClaimResp>('/api/setup/claim', { token: code });
      setCsrf(csrf);
      if (session.setup) session.setup.claimed = true;
      notice = '';
      void go('account');
      return '';
    } catch (e) {
      // Setup finished in another tab: the routes are gone.
      if (e instanceof ApiError && e.status === 404) router.navigate('/login', { replace: true });
      return codeError(e);
    }
  }

  onMount(() => {
    // The code arrives in the URL fragment (never sent to the server by the
    // browser) and is stripped right away, whatever the outcome.
    if (token) {
      router.navigate(location.pathname, { replace: true });
      let t = token;
      try {
        t = decodeURIComponent(t);
      } catch {
        // a mangled escape: the server rejects the raw value
      }
      if (screen === 'wait' && !setupState?.owner_set) {
        void claim(t).then((err) => {
          codeErr = err;
          if (err) screen = 'code';
        });
      }
    }
    if (setupState?.owner_set && screen === 'wait') {
      api.get<Effective>('/api/config').then(
        (c) => (screen = (c.entries.find((e) => e.path === 'models.main')?.value as string[] | undefined)?.length ? 'ready' : 'model'),
        () => (screen = 'model'),
      );
    }
  });
</script>

{#if screen === 'code'}
  <SetupCode install={setupState?.install} {claim} error={codeErr} {notice} />
{:else if screen === 'account'}
  <StepAccount
    bind:account
    oncreated={() => go('model')}
    onexpired={(text) => {
      notice = text;
      codeErr = '';
      if (session.setup) session.setup.claimed = false;
      void go('code');
    }}
  />
{:else if screen === 'model'}
  <StepModel onnext={() => go('ready')} />
{:else if screen === 'ready'}
  <StepReady />
{/if}
