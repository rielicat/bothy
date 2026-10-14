<script lang="ts">
  import { readRepos } from '../../lib/api/knowledge';
  import { router } from '../../lib/router.svelte';
  import { session } from '../../lib/stores/session.svelte';

  // The empty chat (§7.2): a heading, one sentence and up to three suggestions
  // that fill the composer (never send). With GitHub connected the first two
  // name the most recently read repository.
  let { onpick }: { onpick: (text: string) => void } = $props();

  let repo = $state('');
  $effect(() => {
    readRepos()
      .then((rs) => {
        const r = rs.filter((x) => x.status === 'ready').sort((a, b) => b.indexed_at.localeCompare(a.indexed_at))[0];
        if (r) repo = r.repo;
      })
      .catch(() => {});
  });

  const rows = $derived<{ text: string; href?: string }[]>(
    repo
      ? [{ text: `Summarise the open pull requests in ${repo}` }, { text: `What changed in ${repo} this week?` }, { text: 'What can you do for our team?' }]
      : [
          { text: 'Draft a release checklist for our next version' },
          session.isAdmin ? { text: 'Connect GitHub so Bothy can read your code', href: '/settings/connections/github' } : { text: 'Turn these meeting notes into a list of tasks' },
          { text: 'What can you do for our team?' },
        ],
  );
</script>

<div class="pt-[20vh] max-sm:pt-[12vh]">
  <h1 class="text-display text-text" tabindex="-1">What should Bothy work on?</h1>
  <p class="mt-2 max-w-[30rem] text-reading text-muted">Bothy can read your code, draft and research, and it asks before doing anything you'd want to check.</p>
  <ul class="mt-6">
    {#each rows as r (r.text)}
      <li>
        {#if r.href}
          <a
            href={r.href}
            onclick={router.link}
            class="-mx-3 flex min-h-10 items-center rounded-md px-3 text-body text-accent-text underline decoration-accent-text/40 underline-offset-2 hover:bg-subtle hover:decoration-accent-text pointer-coarse:min-h-11">{r.text}</a
          >
        {:else}
          <button
            type="button"
            class="-mx-3 flex min-h-10 w-[calc(100%+1.5rem)] cursor-pointer items-center rounded-md px-3 text-left text-body text-text hover:bg-subtle pointer-coarse:min-h-11"
            onclick={() => onpick(r.text)}>{r.text}</button
          >
        {/if}
      </li>
    {/each}
  </ul>
</div>
