<script lang="ts">
  import Icon from '../../components/Icon.svelte';
  import { api, errorText, setCsrf } from '../../lib/api/client';
  import type { DoctorReport, Me, Session, SetupClaimResp } from '../../lib/api/types.gen';
  import { config } from '../../lib/config.svelte';
  import { strength } from '../../lib/password';
  import { router } from '../../lib/router.svelte';
  import { localZone, timezones } from '../../lib/schedules';
  import { session } from '../../lib/stores/session.svelte';
  import { copyText, toasts } from '../../lib/stores/ui.svelte';
  import StepApps from './StepApps.svelte';
  import StepModels from './StepModels.svelte';
  import StepSafety from './StepSafety.svelte';
  import StepYours from './StepYours.svelte';

  const steps = ['Claim', 'Account', 'Models', 'Safety', 'Chat apps', 'Make it yours', 'Done'];
  const HINT = 'bothy.setup.step';

  function initial(): number {
    const st = session.setup;
    if (!st?.owner_set) return st?.claimed ? 1 : 0;
    let hint = 2;
    try {
      hint = Number(sessionStorage.getItem(HINT)) || 2;
    } catch {
      // storage blocked: start at the first post-account step
    }
    return Math.min(Math.max(hint, 2), 6);
  }

  let step = $state(initial());
  let reached = $state(initial());
  let token = $state('');
  let claiming = $state(false);
  let error = $state('');
  let owner = $state({ username: '', password: '', timezone: session.me?.user.timezone || localZone(), multi: false });
  let show = $state(false);
  let audit = $state<DoctorReport | null>(null);
  let completing = $state(false);

  const pw = $derived(strength(owner.password));
  const zones = timezones();

  $effect(() => {
    try {
      if (step >= 2) sessionStorage.setItem(HINT, String(step));
    } catch {
      // only a resume hint
    }
  });

  function go(n: number) {
    step = n;
    reached = Math.max(reached, n);
    window.scrollTo({ top: 0 });
  }

  // The token arrives in the URL fragment (never sent to the server by the
  // browser) and is stripped right away, whatever the outcome.
  $effect(() => {
    const m = /^#t=([^&]+)/.exec(location.hash);
    if (!m) return;
    history.replaceState(null, '', location.pathname);
    if (step === 0) void claim(decodeURIComponent(m[1]));
  });

  async function claim(t: string) {
    claiming = true;
    error = '';
    try {
      setCsrf((await api.post<SetupClaimResp>('/api/setup/claim', { token: t.trim() })).csrf);
      if (session.setup) session.setup.claimed = true;
      go(1);
    } catch (e) {
      error = errorText(e);
    } finally {
      claiming = false;
    }
  }

  async function createOwner(e: SubmitEvent) {
    e.preventDefault();
    error = '';
    try {
      const me = await api.post<Me>('/api/setup/owner', { username: owner.username.trim(), password: owner.password, timezone: owner.timezone });
      session.login(me);
      if (session.setup) session.setup.owner_set = true;
      owner.password = '';
      await config.load(true);
      await config.set({ timezone: owner.timezone, mode: owner.multi ? 'multi' : 'single' }, true);
      go(2);
    } catch (err) {
      error = errorText(err);
    }
  }

  async function finish() {
    completing = true;
    try {
      await api.post('/api/setup/complete');
      audit = await api.post<DoctorReport>('/api/security-audit', { fix: false }).catch(() => ({ findings: [] }));
      go(6);
    } catch (e) {
      toasts.error(e);
    } finally {
      completing = false;
    }
  }

  async function leave(first?: string) {
    if (session.setup) session.setup.needs_setup = false;
    try {
      sessionStorage.removeItem(HINT);
    } catch {
      // nothing to clean up
    }
    if (!first) return router.navigate('/chat', { replace: true });
    try {
      const s = await api.post<Session>('/api/sessions', {});
      await api.post(`/api/sessions/${encodeURIComponent(s.id)}/messages`, { text: first });
      router.navigate(`/chat/${encodeURIComponent(s.id)}`, { replace: true });
    } catch (e) {
      toasts.error(e);
      router.navigate('/chat', { replace: true });
    }
  }

  const serious = $derived((audit?.findings ?? []).filter((f) => ['critical', 'high', 'medium'].includes(f.severity)));
</script>

<svelte:head><title>Set up Bothy</title></svelte:head>

<main class="min-h-dvh bg-bg px-4 py-8 sm:py-12">
  <div class="mx-auto max-w-2xl">
    <div class="mb-8 flex items-center gap-3">
      <span class="grid size-10 place-items-center rounded-xl bg-accent text-on-accent shadow-sm"><Icon name="bothy" size={22} /></span>
      <div>
        <p class="text-lg font-semibold tracking-tight">Bothy</p>
        <p class="text-xs text-muted">Step {step + 1} of {steps.length} · {steps[step]}</p>
      </div>
    </div>

    <nav aria-label="Setup progress" class="mb-8">
      <ol class="flex items-center gap-1.5">
        {#each steps as label, i (label)}
          {@const done = i < reached || (i === reached && i !== step)}
          <li class="flex-1">
            <button
              type="button"
              class="block h-1.5 w-full rounded-full transition-colors {i === step ? 'bg-accent' : done ? 'bg-accent/40' : 'bg-line'} disabled:cursor-default"
              disabled={i > reached || i === step || (i < 2 && !!session.setup?.owner_set) || i === 6}
              aria-label="{label}{i === step ? ' (current)' : ''}"
              aria-current={i === step ? 'step' : undefined}
              onclick={() => go(i)}
            ></button>
          </li>
        {/each}
      </ol>
    </nav>

    <section class="card p-6 shadow-sm sm:p-8" aria-label={steps[step]}>
      {#if step === 0}
        <h1 class="text-2xl font-semibold tracking-tight">Welcome to your own assistant</h1>
        <p class="mt-2 text-muted">Bothy runs on your machine with your own model keys, remembers what matters, and asks before it does anything risky.</p>
        {#if claiming}
          <p class="mt-8 flex items-center gap-2 text-sm text-muted"><span class="size-2 animate-pulse rounded-full bg-accent"></span>Checking your setup link…</p>
        {:else}
          <form class="mt-8 space-y-3" onsubmit={(e) => (e.preventDefault(), claim(token))}>
            <label class="label" for="setup-token">Setup token</label>
            <input id="setup-token" class="input mono" autocomplete="off" spellcheck="false" required placeholder="Paste the token from the setup link" bind:value={token} />
            {#if error}<p class="alert-bad" role="alert">{error}</p>{/if}
            <button class="btn btn-primary" disabled={!token.trim()}>Continue</button>
          </form>
          <div class="mt-8 rounded-xl bg-sunken p-4 text-sm">
            <p class="font-medium">Where is the token?</p>
            <p class="mt-1 text-muted">Bothy printed a link with it when it first started. Any of these shows it again:</p>
            <ul class="mt-3 space-y-2">
              {#each ['docker logs bothy | grep setup', 'docker exec bothy /bothy setup-token', 'cat <data>/setup-token'] as cmd (cmd)}
                <li class="flex items-center gap-2">
                  <code class="mono min-w-0 flex-1 truncate rounded-md bg-surface px-2 py-1">{cmd}</code>
                  <button type="button" class="btn btn-ghost btn-icon btn-sm" aria-label="Copy command" onclick={() => copyText(cmd)}><Icon name="copy" size={14} /></button>
                </li>
              {/each}
            </ul>
          </div>
        {/if}
      {:else if step === 1}
        <h1 class="text-2xl font-semibold tracking-tight">Create your account</h1>
        <p class="mt-2 text-muted">You'll be the owner. Only people you add can sign in.</p>
        <form class="mt-6 space-y-4" onsubmit={createOwner}>
          <div>
            <label class="label" for="o-user">Username</label>
            <input id="o-user" class="input" autocomplete="username" autocapitalize="none" spellcheck="false" required bind:value={owner.username} />
          </div>
          <div>
            <label class="label" for="o-pass">Password</label>
            <div class="relative">
              <input id="o-pass" class="input pr-10" type={show ? 'text' : 'password'} autocomplete="new-password" minlength="12" required aria-describedby="o-pass-help" bind:value={owner.password} />
              <button type="button" class="absolute inset-y-0 right-0 grid w-10 place-items-center text-muted hover:text-fg" aria-label={show ? 'Hide password' : 'Show password'} aria-pressed={show} onclick={() => (show = !show)}><Icon name={show ? 'eyeOff' : 'eye'} size={17} /></button>
            </div>
            <div class="mt-2 flex items-center gap-2" id="o-pass-help">
              <div class="flex flex-1 gap-1" aria-hidden="true">
                {#each [1, 2, 3, 4] as n (n)}<span class="h-1 flex-1 rounded-full {pw.score >= n ? (pw.score >= 3 ? 'bg-ok' : 'bg-warn') : 'bg-line'}"></span>{/each}
              </div>
              <span class="w-24 text-right text-xs text-muted" aria-live="polite">{owner.password ? pw.label : 'At least 12 characters'}</span>
            </div>
          </div>
          <div>
            <label class="label" for="o-tz">Timezone</label>
            <input id="o-tz" class="input" list="o-zones" bind:value={owner.timezone} />
            <datalist id="o-zones">{#each zones as z (z)}<option value={z}></option>{/each}</datalist>
          </div>
          <label class="flex items-start gap-3 rounded-xl border border-line p-3 text-sm">
            <input type="checkbox" class="switch mt-0.5" bind:checked={owner.multi} />
            <span><span class="font-medium">Household or team mode</span><span class="block text-muted">Add other people later, each with their own chats, memory and spend cap.</span></span>
          </label>
          {#if error}<p class="alert-bad" role="alert">{error}</p>{/if}
          <button class="btn btn-primary" disabled={owner.password.length < 12 || !owner.username.trim()}>Create account</button>
        </form>
      {:else if step === 2}
        <StepModels onnext={() => go(3)} />
      {:else if step === 3}
        <StepSafety onnext={() => go(4)} />
      {:else if step === 4}
        <StepApps onnext={() => go(5)} />
      {:else if step === 5}
        <StepYours onnext={finish} busy={completing} />
      {:else}
        <div class="text-center">
          <span class="mx-auto grid size-14 place-items-center rounded-2xl bg-ok-soft text-ok"><Icon name="check" size={28} /></span>
          <h1 class="mt-4 text-2xl font-semibold tracking-tight">Bothy is ready</h1>
          <p class="mt-2 text-muted">You can change anything later under Settings.</p>
        </div>
        {#if audit}
          <div class="mt-6 rounded-xl border border-line p-4 text-sm">
            <p class="font-medium">{serious.length ? `Security check: ${serious.length} thing${serious.length === 1 ? '' : 's'} to look at` : 'Security check: all good'}</p>
            <ul class="mt-2 space-y-1.5">
              {#each serious.slice(0, 3) as f (f.id)}<li class="flex gap-2 text-muted"><Icon name="alert" size={15} class="mt-0.5 text-warn" />{f.title}</li>{/each}
            </ul>
            {#if serious.length}<p class="mt-2 text-xs text-muted">Details and fixes are under Settings → Advanced.</p>{/if}
          </div>
        {/if}
        <p class="mt-6 mb-2 text-sm font-medium">Try one of these</p>
        <div class="grid gap-2">
          {#each ['What can you do, and what will you always ask me first?', "Summarize today's top tech news in five bullets", 'Help me plan a calm, productive week'] as t (t)}
            <button type="button" class="card px-4 py-3 text-left text-sm hover:border-accent hover:bg-accent-soft/40" onclick={() => leave(t)}>{t}</button>
          {/each}
        </div>
        <button type="button" class="btn btn-primary mt-6 w-full" onclick={() => leave()}>Open chat</button>
      {/if}
    </section>
  </div>
</main>
