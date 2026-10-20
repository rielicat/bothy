<script lang="ts">
  import Button from '../../components/ui/Button.svelte';
  import CommandBlock from '../../components/ui/CommandBlock.svelte';
  import Disclosure from '../../components/ui/Disclosure.svelte';
  import Notice from '../../components/ui/Notice.svelte';
  import TextField from '../../components/ui/TextField.svelte';
  import { setupCommands, type Install } from './auth';
  import Frame from './Frame.svelte';

  // The setup code gate (docs/design/ui.md §5.3): not a step, so no "Step N of 3".
  let {
    install,
    claim,
    error: initial = '',
    notice = '',
  }: {
    install?: Install;
    /** Resolves to an error sentence, or '' once the code is accepted. */
    claim: (code: string) => Promise<string>;
    error?: string;
    notice?: string;
  } = $props();

  let code = $state('');
  // svelte-ignore state_referenced_locally
  let error = $state(initial);
  let busy = $state(false);
  let input: HTMLInputElement | undefined = $state();
  const cmds = $derived(setupCommands(install));

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    error = code.trim() ? '' : 'Paste the setup code first.';
    if (!error) {
      busy = true;
      error = await claim(code.trim());
      busy = false;
    }
    if (error) input?.focus();
  }
</script>

<Frame title="Set up Bothy" intro="Paste the setup code Bothy printed when it started." card>
  {#if notice}<Notice class="mb-6" tone="warning" text={notice} />{/if}
  <form novalidate onsubmit={submit}>
    <TextField label="Setup code" bind:value={code} bind:input {error} autocomplete="off" autocapitalize="none" spellcheck="false" oninput={() => (error = '')} />
    <Button type="submit" variant="primary" class="mt-6 h-11 w-full" loading={busy} loadingLabel="Checking…">Continue</Button>
  </form>

  <p class="mt-10 text-body text-muted">Lost the code? Run this on the server to see it again:</p>
  <CommandBlock class="mt-2" code={cmds.main} />
  <Disclosure class="mt-3" label={cmds.more}>
    <div class="space-y-3">
      {#each cmds.rest as c (c.cmd)}<CommandBlock label={c.label} code={c.cmd} />{/each}
    </div>
  </Disclosure>
</Frame>
