<script lang="ts">
  import CommandBlock from '../../components/ui/CommandBlock.svelte';
  import { tunnel } from './auth';
  import Frame from './Frame.svelte';

  // Setup routes answer other computers with 403 (docs/design/ui.md §5.4); `fix` is the server's fix line.
  let { fix }: { fix?: string } = $props();
  const t = $derived(tunnel(fix));
</script>

<Frame
  title="Finish setup on the server"
  intro="For safety, setup only works from the computer Bothy runs on. Connect to it with a secure tunnel, then open the setup link there."
>
  <CommandBlock code={t.cmd} />
  <p class="mt-3 text-body text-muted">Then open <code class="text-code text-text" data-copy-ignore>{t.url}</code> in your browser.</p>
</Frame>
