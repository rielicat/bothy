<script lang="ts">
  import { onMount } from 'svelte';
  import { api } from '../../lib/api/client';
  import type { MemoryDoc, ModelInfo } from '../../lib/api/types.gen';
  import { config } from '../../lib/config.svelte';
  import { usd } from '../../lib/format';
  import { toasts } from '../../lib/stores/ui.svelte';

  let { onnext, busy = false }: { onnext: () => void; busy?: boolean } = $props();

  const tones = [
    { v: 'warm', label: 'Warm and brief', line: 'Be warm, brief and practical. Use plain language.' },
    { v: 'direct', label: 'Straight to the point', line: 'Be direct and concise. Lead with the answer and skip pleasantries.' },
    { v: 'playful', label: 'Friendly and playful', line: 'Be friendly and a little playful, but never at the cost of clarity.' },
    { v: 'formal', label: 'Calm and formal', line: 'Be courteous and precise, like a professional assistant.' },
  ];

  let name = $state('Bothy');
  let tone = $state('warm');
  let about = $state({ call: '', work: '', remember: '' });
  let heartbeat = $state(false);
  let models = $state<ModelInfo[]>([]);
  let saving = $state(false);

  const fastRef = $derived(config.value<string[]>('models.fast', [])?.[0] ?? '');
  const fast = $derived(models.find((m) => `${m.ref.provider}/${m.ref.model}` === fastRef));
  // 28 checks a day (every 30 min, 08:00–22:00), each ~2K tokens in and ~100 out.
  const perDay = $derived(fast?.price.known ? (28 * (2000 * fast.price.in_per_m + 100 * fast.price.out_per_m)) / 1e6 : 0);

  // A resumed wizard can open on this step before anything loaded config.
  onMount(() => {
    api.get<ModelInfo[]>('/api/models').then((m) => (models = m), () => {});
    void config.load().then(() => (heartbeat = config.flag('scheduler.heartbeat.enabled')));
  });

  async function save() {
    saving = true;
    try {
      const n = name.trim() || 'Bothy';
      const soul = `# ${n}\n\nYou are ${n}, a personal assistant running on your owner's own machine.\n${tones.find((t) => t.v === tone)!.line}\nSay plainly what you did and did not do. Ask before acting on anything that leaves this machine.\n`;
      await api.put('/api/persona', { content: soul });
      const lines = [about.call && `- Call me ${about.call.trim()}.`, about.work && `- ${about.work.trim()}`, about.remember && `- ${about.remember.trim()}`].filter(Boolean);
      if (lines.length) {
        const doc = await api.get<MemoryDoc>('/api/memory/USER');
        await api.put('/api/memory/USER', { content: [doc.content.trim(), ...lines].filter(Boolean).join('\n') }, { 'If-Match': doc.etag });
      }
      if (heartbeat !== (config.flag('scheduler.heartbeat.enabled'))) await config.set({ 'scheduler.heartbeat.enabled': heartbeat }, true);
      onnext();
    } catch (e) {
      toasts.error(e);
    } finally {
      saving = false;
    }
  }
</script>

<h1 class="text-2xl font-semibold tracking-tight">Make it yours</h1>
<p class="mt-2 text-muted">A name, a voice and a few facts. All optional, all editable later.</p>

<div class="mt-6 grid gap-4 sm:grid-cols-2">
  <div>
    <label class="label" for="y-name">Assistant name</label>
    <input id="y-name" class="input" maxlength="40" bind:value={name} />
  </div>
  <div>
    <label class="label" for="y-tone">Tone</label>
    <select id="y-tone" class="input" bind:value={tone}>{#each tones as t (t.v)}<option value={t.v}>{t.label}</option>{/each}</select>
  </div>
</div>

<h2 class="mt-8 text-sm font-semibold">About you <span class="font-normal text-muted">(saved to USER.md)</span></h2>
<div class="mt-3 space-y-3">
  <div><label class="label" for="y-call">What should {name || 'Bothy'} call you?</label><input id="y-call" class="input" bind:value={about.call} /></div>
  <div><label class="label" for="y-work">What do you do, in a sentence?</label><input id="y-work" class="input" placeholder="I run a small design studio and have two kids." bind:value={about.work} /></div>
  <div><label class="label" for="y-rem">Anything it should always keep in mind?</label><input id="y-rem" class="input" placeholder="Keep answers short unless I ask for detail." bind:value={about.remember} /></div>
</div>

<label class="mt-8 flex items-start gap-3 rounded-xl border border-line p-4 text-sm">
  <input type="checkbox" class="switch mt-0.5" bind:checked={heartbeat} />
  <span>
    <span class="font-medium">Heartbeat</span>
    <span class="block text-muted">Every 30 minutes in the daytime, {name || 'Bothy'} reads your HEARTBEAT.md checklist and only messages you when something needs attention.{perDay ? ` About ${usd(perDay)} a day on ${fast?.display || fastRef}.` : ''}</span>
  </span>
</label>

<div class="mt-8 flex justify-end">
  <button type="button" class="btn btn-primary" disabled={saving || busy} onclick={save}>{saving || busy ? 'Finishing…' : 'Finish setup'}</button>
</div>
