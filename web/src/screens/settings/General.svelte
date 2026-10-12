<script lang="ts">
  import ConfigField from '../../components/ConfigField.svelte';
  import Icon from '../../components/Icon.svelte';
  import Section from '../../components/Section.svelte';
  import { api, errorText } from '../../lib/api/client';
  import { timezones } from '../../lib/schedules';
  import { session } from '../../lib/stores/session.svelte';
  import { toasts } from '../../lib/stores/ui.svelte';

  const zones = timezones().map((z): [string, string] => [z, z]);
  const host = location.hostname;
  const tls = location.protocol === 'https:';
  const peer = /^(localhost|127\.|\[::1\]$)/.test(host) ? 'this computer' : /^(10\.|192\.168\.|172\.(1[6-9]|2\d|3[01])\.|\[f[cd])/i.test(host) ? 'your local network' : 'the internet';

  let old = $state('');
  let next = $state('');
  let again = $state('');
  let error = $state('');

  async function changePassword(e: SubmitEvent) {
    e.preventDefault();
    error = next !== again ? 'The new passwords do not match.' : next.length < 12 ? 'Use at least 12 characters.' : '';
    if (error) return;
    try {
      await api.post('/api/auth/password', { old, new: next });
      old = next = again = '';
      toasts.ok('Password changed. Other sessions stay signed in until they expire.');
    } catch (err) {
      error = errorText(err);
    }
  }
</script>

<Section title="Remote access" desc="How this browser reaches Bothy.">
  <div class="flex items-start gap-3 py-4 text-sm">
    <span class="grid size-9 place-items-center rounded-lg {tls ? 'bg-ok-soft text-ok' : peer === 'this computer' ? 'bg-info-soft text-info' : 'bg-warn-soft text-warn'}">
      <Icon name={tls ? 'lock' : 'globe'} size={18} />
    </span>
    <div>
      <p class="font-medium">Connected from {peer} over {tls ? 'HTTPS' : 'plain HTTP'}</p>
      <p class="mt-0.5 text-muted">
        {tls
          ? 'Traffic is encrypted. Cookies use the __Host- prefix.'
          : peer === 'this computer'
            ? 'Plain HTTP is fine on this computer. To reach Bothy from elsewhere, put it behind Tailscale or a TLS proxy such as Caddy.'
            : 'Plain HTTP outside this computer is only allowed with insecure_lan. Prefer Tailscale or a TLS proxy.'}
      </p>
    </div>
  </div>
</Section>

{#if session.isAdmin}
  <Section title="Instance">
    <ConfigField path="mode" label="Mode" help="Household/team mode adds users, roles and per-person spend caps." options={[['single', 'Just me'], ['multi', 'Household or team']]} />
    <ConfigField path="timezone" label="Timezone" help="Used for schedules and the daily spend reset." type="select" options={zones} />
    <ConfigField path="server.public_url" label="Public URL" help="The https address people use to reach Bothy. Approval cards link here." placeholder="https://bothy.example.com" />
    <ConfigField path="server.allowed_hosts" label="Extra host names" help="Other Host headers to accept besides the public URL and localhost." />
    <ConfigField path="log.level" label="Log level" />
  </Section>
{/if}

<Section title="Your password">
  <form class="grid gap-3 py-4 sm:max-w-sm" onsubmit={changePassword}>
    <input type="text" class="hidden" autocomplete="username" aria-label="Username" tabindex="-1" readonly value={session.me?.user.username} />
    <label class="label" for="pw-old">Current password</label>
    <input id="pw-old" class="input -mt-2" type="password" autocomplete="current-password" required bind:value={old} />
    <label class="label" for="pw-new">New password</label>
    <input id="pw-new" class="input -mt-2" type="password" autocomplete="new-password" minlength="12" required bind:value={next} />
    <label class="label" for="pw-again">Repeat new password</label>
    <input id="pw-again" class="input -mt-2" type="password" autocomplete="new-password" required bind:value={again} />
    {#if error}<p class="alert-bad" role="alert">{error}</p>{/if}
    <button class="btn btn-primary justify-self-start">Change password</button>
  </form>
</Section>
