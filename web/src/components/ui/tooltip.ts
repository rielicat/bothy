// Tooltip behaviour as an attachment, so IconButton and Tooltip share it
// without wrapping the trigger. Fine pointers only (§15.3 row 33): 400 ms on
// hover, immediately on keyboard focus, Esc dismisses.
import type { Attachment } from 'svelte/attachments';
import { finePointer, focusables, place, popoverAttr, showLayer } from './layer';

let seq = 0;

const TIP = 'pointer-events-none fixed inset-auto z-50 m-0 max-w-64 rounded-sm border-0 bg-text px-2 py-1 text-small text-bg shadow-[var(--shadow-overlay)]';

function focusVisible(el: Element): boolean {
  try {
    return el.matches(':focus-visible');
  } catch {
    return true;
  }
}

export interface TooltipOptions {
  /** Links the tooltip to the trigger with aria-describedby (off when it repeats the trigger's own label). */
  describe?: boolean;
  placement?: 'top' | 'bottom';
}

export function tooltip(text: () => string | undefined, opts: TooltipOptions = {}): Attachment<HTMLElement> {
  return (node) => {
    let tip: HTMLDivElement | null = null;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let shown = false;

    const ensure = () => {
      if (!tip) {
        tip = document.createElement('div');
        tip.id = `tip-${++seq}`;
        tip.setAttribute('role', 'tooltip');
        if (popoverAttr) tip.setAttribute('popover', popoverAttr);
        tip.hidden = true;
        tip.className = TIP;
      }
      // Inside a modal dialog the rest of the page is inert, so the tip lives in the dialog.
      const host = node.closest('dialog') ?? document.body;
      if (tip.parentNode !== host) host.appendChild(tip);
      tip.textContent = text() ?? '';
      return tip;
    };

    const target = () => (node.matches('a[href], button, input, select, textarea, [tabindex]') ? node : focusables(node)[0]);

    if (opts.describe && text()) {
      const t = ensure();
      const el = target();
      if (el && el.getAttribute('aria-label') !== t.textContent) el.setAttribute('aria-describedby', t.id);
    }

    const show = () => {
      clearTimeout(timer);
      if (!text() || shown) return;
      const t = ensure();
      t.hidden = false;
      showLayer(t);
      place(node, t, opts.placement === 'bottom' ? 'bottom' : 'top', { offset: 6 });
      shown = true;
    };
    const hide = () => {
      clearTimeout(timer);
      if (!shown || !tip) return;
      if (typeof tip.hidePopover === 'function' && tip.hasAttribute('popover')) {
        try {
          tip.hidePopover();
        } catch {
          // not showing
        }
      }
      tip.hidden = true;
      shown = false;
    };

    const enter = () => {
      if (!finePointer()) return;
      clearTimeout(timer);
      timer = setTimeout(show, 400);
    };
    const focusIn = (e: FocusEvent) => {
      if (finePointer() && e.target instanceof Element && focusVisible(e.target)) show();
    };
    const key = (e: KeyboardEvent) => {
      if (e.key === 'Escape' && shown) {
        e.stopPropagation();
        hide();
      }
    };

    node.addEventListener('pointerenter', enter);
    node.addEventListener('pointerleave', hide);
    node.addEventListener('pointerdown', hide);
    node.addEventListener('focusin', focusIn);
    node.addEventListener('focusout', hide);
    node.addEventListener('keydown', key);
    return () => {
      clearTimeout(timer);
      node.removeEventListener('pointerenter', enter);
      node.removeEventListener('pointerleave', hide);
      node.removeEventListener('pointerdown', hide);
      node.removeEventListener('focusin', focusIn);
      node.removeEventListener('focusout', hide);
      node.removeEventListener('keydown', key);
      tip?.remove();
    };
  };
}
