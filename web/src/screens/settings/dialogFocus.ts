import { tick } from 'svelte';
import { focusables } from '../../components/ui/layer';

/**
 * When a dialog swaps its form for a result (a new token, a one-time
 * password), the focused submit button disappears and focus falls to the
 * page behind the dialog. This puts it on the result's first control, the
 * Copy button, or else on the footer.
 */
export async function focusDialogResult(): Promise<void> {
  await tick();
  const dialog = document.querySelector('dialog[open]');
  if (!dialog) return;
  const body = dialog.querySelector('[data-dialog-body]');
  const foot = dialog.querySelector('[data-dialog-footer]');
  ((body && focusables(body)[0]) ?? (foot && focusables(foot)[0]))?.focus();
}
