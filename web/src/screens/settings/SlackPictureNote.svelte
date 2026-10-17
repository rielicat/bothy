<script lang="ts">
  import Icon from '../../components/Icon.svelte';
  import { pictureDownloadURL, slackAppURL, slackPictureLine } from './picture';

  let {
    appId,
    download = true,
    todo = false,
  }: {
    /** Slack's id for the app (ChannelStatus.app_id), for a link straight to its settings. */
    appId?: string;
    /** Off where a "Download picture" button sits next to it already. */
    download?: boolean;
    /** Right after a change, when Slack still shows the old picture. */
    todo?: boolean;
  } = $props();

  const link = 'link -my-0.5 inline-flex min-h-6 items-center pointer-coarse:-my-3 pointer-coarse:min-h-11';
</script>

<p class="flex items-start gap-1.5 {todo ? 'text-warning-text' : ''}">
  {#if todo}<Icon name="alert" class="mt-0.5 shrink-0" />{/if}<span class="min-w-0">{slackPictureLine}</span>
</p>
<!-- Under the warning icon, the links line up with the sentence. -->
<p class="mt-0.5 flex flex-wrap gap-x-3 {todo ? 'pl-5.5' : ''}">
  {#if download}<a class={link} href={pictureDownloadURL} download>Download picture</a>{/if}
  <a class={link} href={slackAppURL(appId)} target="_blank" rel="noopener noreferrer"
    >Open Slack's app settings<span aria-hidden="true">&nbsp;↗</span><span class="sr-only"> (opens in a new tab)</span></a
  >
</p>
