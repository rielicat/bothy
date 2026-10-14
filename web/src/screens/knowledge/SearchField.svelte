<script lang="ts">
  import Icon from '../../components/Icon.svelte';
  import IconButton from '../../components/ui/IconButton.svelte';

  let {
    value = $bindable(''),
    onsearch,
    clearable = false,
    class: cls = '',
  }: { value?: string; onsearch: (q: string) => void; clearable?: boolean; class?: string } = $props();

  const uid = $props.id();
  let input: HTMLInputElement | undefined = $state();
</script>

<form
  role="search"
  class="relative {cls}"
  onsubmit={(e) => {
    e.preventDefault();
    onsearch(value.trim());
  }}
>
  <label for="{uid}-q" class="sr-only">Search code and notes</label>
  <Icon name="search" class="pointer-events-none absolute top-1/2 left-3 -translate-y-1/2 text-muted" />
  <!-- The browser's own clear button is hidden: this field has its own, sized for touch. -->
  <input
    bind:this={input}
    bind:value
    id="{uid}-q"
    type="search"
    enterkeyhint="search"
    autocomplete="off"
    placeholder="Search code and notes"
    class="input pl-9 [&::-webkit-search-cancel-button]:appearance-none {clearable ? 'pr-10 pointer-coarse:pr-12' : ''}"
  />
  {#if clearable && value}
    <IconButton
      icon="close"
      label="Clear search"
      size="sm"
      variant="quiet"
      class="absolute top-1/2 right-0.5 -translate-y-1/2 pointer-coarse:right-0"
      onclick={() => {
        value = '';
        input?.focus();
      }}
    />
  {/if}
</form>
