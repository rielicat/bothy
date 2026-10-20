import { fireEvent, render, screen } from '@testing-library/svelte';
import { createRawSnippet, flushSync } from 'svelte';
import { describe, expect, it, vi } from 'vitest';
import { initials } from './Avatar.svelte';
import Button from './Button.svelte';
import CommandBlock from './CommandBlock.svelte';
import Disclosure from './Disclosure.svelte';
import FieldError from './FieldError.svelte';
import { place } from './layer';
import { announce, live } from './live.svelte';
import Segmented from './Segmented.svelte';
import SettingsRow from './SettingsRow.svelte';
import Switch from './Switch.svelte';
import Tabs, { currentTab } from './Tabs.svelte';

const text = (s: string) => createRawSnippet(() => ({ render: () => `<span>${s}</span>` }));

describe('Button', () => {
  it('stays focusable when disabled with a reason, links the reason and ignores presses', async () => {
    const onclick = vi.fn();
    const onblocked = vi.fn();
    render(Button, { props: { variant: 'primary', reason: 'Connect a model to continue.', onclick, onblocked, children: text('Continue') } });
    const button = screen.getByRole('button', { name: 'Continue' });
    expect(button.getAttribute('aria-disabled')).toBe('true');
    expect(button.hasAttribute('disabled')).toBe(false);
    const reason = document.getElementById(button.getAttribute('aria-describedby')!);
    expect(reason?.textContent).toBe('Connect a model to continue.');
    await fireEvent.click(button);
    expect(onclick).not.toHaveBeenCalled();
    expect(onblocked).toHaveBeenCalledOnce();
  });

  it('does not submit a form while disabled', async () => {
    const submit = vi.fn((e: Event) => e.preventDefault());
    const form = document.createElement('form');
    form.addEventListener('submit', submit);
    document.body.append(form);
    render(Button, { target: form, props: { type: 'submit', disabled: true, children: text('Save') } });
    await fireEvent.click(screen.getByRole('button', { name: 'Save' }));
    expect(submit).not.toHaveBeenCalled();
    form.remove();
  });

  it('locks its width and swaps the label while loading', async () => {
    const rect = vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockReturnValue({ width: 132 } as DOMRect);
    const onclick = vi.fn();
    const { rerender } = render(Button, { props: { loading: false, loadingLabel: 'Saving…', onclick, children: text('Save changes') } });
    const button = screen.getByRole('button', { name: 'Save changes' });
    expect(button.style.minWidth).toBe('');
    await rerender({ loading: true });
    expect(button.getAttribute('aria-busy')).toBe('true');
    expect(button.textContent).toContain('Saving…');
    expect(button.textContent).not.toContain('Save changes');
    expect(button.style.minWidth).toBe('132px');
    await fireEvent.click(button);
    expect(onclick).not.toHaveBeenCalled();
    await rerender({ loading: false });
    expect(button.style.minWidth).toBe('');
    expect(button.hasAttribute('aria-busy')).toBe(false);
    rect.mockRestore();
  });

  it('renders a link for href and marks external ones', () => {
    render(Button, { props: { href: 'https://github.com/acme/web', external: true, children: text('View on GitHub') } });
    const link = screen.getByRole('link', { name: /View on GitHub/ });
    expect(link.getAttribute('target')).toBe('_blank');
    expect(link.getAttribute('rel')).toContain('noopener');
    expect(link.textContent).toContain('↗');
  });

  it('leaves a download link to the browser instead of routing it', async () => {
    render(Button, { props: { href: '/api/agent/picture?download=1', download: true, children: text('Download picture') } });
    const link = screen.getByRole('link', { name: 'Download picture' });
    expect(link.getAttribute('download')).toBe('');
    const click = new MouseEvent('click', { bubbles: true, cancelable: true, button: 0 });
    link.dispatchEvent(click);
    expect(click.defaultPrevented).toBe(false);
  });
});

describe('FieldError', () => {
  it('announces why an action failed, and draws nothing without a reason', async () => {
    const { container, rerender } = render(FieldError, { text: "Couldn't save. Try again.", class: 'mb-2' });
    const p = screen.getByRole('alert');
    expect(p.textContent).toBe("Couldn't save. Try again.");
    expect(p.classList.contains('field-error') && p.classList.contains('mb-2')).toBe(true);
    await rerender({ text: false });
    expect(container.textContent).toBe('');
  });
});

describe('Switch', () => {
  it('is a switch that flips aria-checked', async () => {
    const onchange = vi.fn();
    render(Switch, { props: { label: 'Refine answers', onchange } });
    const sw = screen.getByRole('switch', { name: 'Refine answers' });
    expect(sw.getAttribute('aria-checked')).toBe('false');
    await fireEvent.click(sw);
    expect(sw.getAttribute('aria-checked')).toBe('true');
    expect(onchange).toHaveBeenCalledWith(true);
    await fireEvent.click(screen.getByText('Refine answers'));
    expect(sw.getAttribute('aria-checked')).toBe('false');
  });

  it('says why it is locked and stays put', async () => {
    render(Switch, { props: { label: 'Running code', reason: 'Off on this server.' } });
    const sw = screen.getByRole('switch', { name: 'Running code' });
    expect(sw.getAttribute('aria-disabled')).toBe('true');
    expect(document.getElementById(sw.getAttribute('aria-describedby')!)?.textContent).toBe('Off on this server.');
    await fireEvent.click(sw);
    expect(sw.getAttribute('aria-checked')).toBe('false');
  });

  it('can take its name from a row label', () => {
    render(Switch, { props: { label: 'Check-in', hideLabel: true, checked: true } });
    expect(screen.getByRole('switch', { name: 'Check-in' }).getAttribute('aria-checked')).toBe('true');
    expect(screen.queryByText('Check-in')).toBeNull();
  });
});

describe('Disclosure', () => {
  it('toggles aria-expanded and renders its content only when open', async () => {
    render(Disclosure, { props: { children: text('401 invalid x-api-key') } });
    const button = screen.getByRole('button', { name: 'Details' });
    expect(button.getAttribute('aria-expanded')).toBe('false');
    const panel = document.getElementById(button.getAttribute('aria-controls')!)!;
    expect(panel.hasAttribute('data-details')).toBe(true);
    expect(panel.textContent).toBe('');
    await fireEvent.click(button);
    expect(button.getAttribute('aria-expanded')).toBe('true');
    expect(panel.textContent).toContain('401 invalid x-api-key');
    await fireEvent.click(button);
    expect(button.getAttribute('aria-expanded')).toBe('false');
    expect(panel.textContent).toBe('');
  });

  it('marks only Details content as technical', () => {
    render(Disclosure, { props: { label: 'More options', variant: 'section', open: true, children: text('Branch') } });
    const button = screen.getByRole('button', { name: 'More options' });
    expect(button.getAttribute('aria-expanded')).toBe('true');
    expect(document.getElementById(button.getAttribute('aria-controls')!)!.hasAttribute('data-details')).toBe(false);
  });
});

describe('Segmented', () => {
  it('is a radio group with one tab stop and arrow keys', async () => {
    const onchange = vi.fn();
    render(Segmented, {
      props: {
        label: 'Period',
        value: '7',
        onchange,
        options: [
          { value: '7', label: '7 days' },
          { value: '30', label: '30 days' },
        ],
      },
    });
    expect(screen.getByRole('radiogroup', { name: 'Period' })).toBeTruthy();
    const [seven, thirty] = screen.getAllByRole('radio');
    expect(seven.getAttribute('aria-checked')).toBe('true');
    expect([seven.tabIndex, thirty.tabIndex]).toEqual([0, -1]);
    await fireEvent.keyDown(seven, { key: 'ArrowRight' });
    expect(onchange).toHaveBeenCalledWith('30');
    expect(document.activeElement).toBe(thirty);
    expect(thirty.getAttribute('aria-checked')).toBe('true');
    await fireEvent.keyDown(thirty, { key: 'ArrowRight' });
    expect(onchange).toHaveBeenLastCalledWith('7');
  });
});

describe('SettingsRow', () => {
  it('labels the control, swaps help for the error and announces "Saved" for 2 s', async () => {
    vi.useFakeTimers();
    const children = createRawSnippet((c: () => { id: string; describedBy?: string }) => ({
      render: () => `<input id="${c().id}" aria-describedby="${c().describedBy ?? ''}">`,
    }));
    const { rerender } = render(SettingsRow, { props: { label: 'Daily limit', help: 'New work waits until midnight.', status: null, children } });
    const input = screen.getByLabelText('Daily limit');
    expect(document.getElementById(input.getAttribute('aria-describedby')!)?.textContent).toBe('New work waits until midnight.');
    await rerender({ status: 'saved' });
    const live = screen.getByText('Saved');
    expect(live.getAttribute('aria-live')).toBe('polite');
    vi.advanceTimersByTime(2100);
    flushSync();
    expect(screen.queryByText('Saved')).toBeNull();
    await rerender({ error: "Couldn't save. Try again." });
    expect(screen.queryByText('New work waits until midnight.')).toBeNull();
    expect(screen.getByText("Couldn't save. Try again.")).toBeTruthy();
    vi.useRealTimers();
  });
});

describe('small helpers', () => {
  it('announce() repeats a message by changing the region text', () => {
    announce('Copied');
    const first = live.polite;
    announce('Copied');
    expect(live.polite).not.toBe(first);
    expect(live.polite.trim()).toBe('Copied');
    announce('Approved. Next: Deploy main to staging', { assertive: true });
    expect(live.assertive).toBe('Approved. Next: Deploy main to staging');
  });

  it('finds the current route tab, preferring an exact query match', () => {
    const tabs = [
      { label: 'Waiting', href: '/approvals' },
      { label: 'Done', href: '/approvals?tab=done' },
    ];
    expect(currentTab(tabs, '/approvals', '')).toBe('/approvals');
    expect(currentTab(tabs, '/approvals', '?tab=done')).toBe('/approvals?tab=done');
    expect(currentTab(tabs, '/approvals', '?q=x')).toBe('/approvals');
    expect(currentTab(tabs, '/activity', '')).toBeUndefined();
  });

  it('makes initials from names', () => {
    expect(initials('Alex Kim')).toBe('AK');
    expect(initials('priya')).toBe('P');
    expect(initials('sam.patel')).toBe('SP');
    expect(initials('  ')).toBe('?');
  });

  it('places a layer below its anchor and flips it above near the bottom edge', () => {
    const anchor = document.createElement('button');
    const el = document.createElement('div');
    Object.defineProperty(window, 'innerHeight', { value: 800, configurable: true });
    vi.spyOn(anchor, 'getBoundingClientRect').mockReturnValue({ top: 100, bottom: 136, left: 50, right: 86, width: 36, height: 36 } as DOMRect);
    vi.spyOn(el, 'getBoundingClientRect').mockReturnValue({ width: 192, height: 200 } as DOMRect);
    place(anchor, el, 'bottom-start');
    expect([el.style.top, el.style.left]).toEqual(['140px', '50px']);
    vi.spyOn(anchor, 'getBoundingClientRect').mockReturnValue({ top: 700, bottom: 736, left: 50, right: 86, width: 36, height: 36 } as DOMRect);
    place(anchor, el, 'bottom-start');
    expect(el.style.top).toBe('496px');
  });

  it('narrows an end-aligned layer to the room left of its anchor, so their right edges line up', () => {
    const anchor = document.createElement('button');
    const el = document.createElement('div');
    vi.spyOn(anchor, 'getBoundingClientRect').mockReturnValue({ top: 700, bottom: 732, left: 250, right: 330, width: 80, height: 32 } as DOMRect);
    const rect = vi.spyOn(el, 'getBoundingClientRect').mockReturnValue({ width: 344, height: 200 } as DOMRect);
    place(anchor, el, 'top-end');
    expect(el.style.maxWidth).toBe('322px');
    rect.mockReturnValue({ width: 200, height: 200 } as DOMRect);
    place(anchor, el, 'top-end');
    expect([el.style.maxWidth, el.style.left]).toEqual(['', '130px']);
  });
});

describe('CommandBlock', () => {
  it('always shows Copy, keeps each word whole and copies the command byte for byte', async () => {
    const cmd = 'docker compose  exec bothy /bothy setup-token';
    const writeText = vi.fn(() => Promise.resolve());
    Object.assign(navigator, { clipboard: { writeText } });
    const { container } = render(CommandBlock, { props: { code: cmd } });
    const copy = screen.getByRole('button', { name: 'Copy' });
    expect(copy.className).not.toMatch(/(^|\s)opacity-0/);
    const words = [...container.querySelectorAll('pre span.inline-block')].map((w) => w.textContent);
    expect(words).toEqual(['docker', 'compose', 'exec', 'bothy', '/bothy', 'setup-token']);
    expect(container.querySelector('pre')!.textContent).toBe(cmd);
    await fireEvent.click(copy);
    expect(writeText).toHaveBeenCalledWith(cmd);
  });
});

describe('Tabs', () => {
  it('brings the current tab into view, so a phone shows which one is selected', () => {
    // scrollIntoView would move Chrome's focus starting point, so the row scrolls itself.
    const into = vi.fn();
    const saved = {
      into: Element.prototype.scrollIntoView,
      rect: Element.prototype.getBoundingClientRect,
      ...Object.fromEntries((['scrollWidth', 'clientWidth', 'scrollLeft'] as const).map((k) => [k, Object.getOwnPropertyDescriptor(Element.prototype, k)])),
    };
    const left = new WeakMap<Element, number>();
    const isRow = (el: Element) => el.classList.contains('tabs');
    Element.prototype.scrollIntoView = into;
    Object.defineProperty(Element.prototype, 'scrollWidth', { configurable: true, get() { return isRow(this) ? 600 : 0; } });
    Object.defineProperty(Element.prototype, 'clientWidth', { configurable: true, get() { return isRow(this) ? 300 : 0; } });
    Object.defineProperty(Element.prototype, 'scrollLeft', { configurable: true, get() { return left.get(this) ?? 0; }, set(v: number) { left.set(this, v); } });
    Element.prototype.getBoundingClientRect = function (this: Element) {
      const [l, r] = isRow(this) ? [0, 300] : this.getAttribute('aria-current') ? [500, 580] : [0, 0];
      return { left: l, right: r, top: 0, bottom: 0, width: r - l, height: 0, x: l, y: 0, toJSON: () => ({}) } as DOMRect;
    };
    try {
      const tabs = ['Tasks', 'Schedules', 'Releases', 'Spending', 'Audit log'].map((label) => ({ label, href: `/activity/${label.toLowerCase()}` }));
      const { container } = render(Tabs, { props: { tabs, current: '/activity/audit log' } });
      expect(container.querySelector('.tabs')!.scrollLeft).toBe(280);
      expect(into).not.toHaveBeenCalled();
    } finally {
      Element.prototype.scrollIntoView = saved.into;
      Element.prototype.getBoundingClientRect = saved.rect;
      for (const k of ['scrollWidth', 'clientWidth', 'scrollLeft'] as const) {
        const d = (saved as Record<string, PropertyDescriptor | undefined>)[k];
        if (d) Object.defineProperty(Element.prototype, k, d);
        else delete (Element.prototype as unknown as Record<string, unknown>)[k];
      }
    }
  });

  it('brings the current tab into view again when tabs arrive later and the row starts to overflow', async () => {
    // Each tab is 150 px wide in a 300 px row; Activity adds Releases once it has loaded.
    const saved = {
      rect: Element.prototype.getBoundingClientRect,
      ...Object.fromEntries((['scrollWidth', 'clientWidth', 'scrollLeft'] as const).map((k) => [k, Object.getOwnPropertyDescriptor(Element.prototype, k)])),
    };
    const left = new WeakMap<Element, number>();
    const isRow = (el: Element) => el.classList.contains('tabs');
    Object.defineProperty(Element.prototype, 'scrollWidth', { configurable: true, get() { return isRow(this) ? this.children.length * 150 : 0; } });
    Object.defineProperty(Element.prototype, 'clientWidth', { configurable: true, get() { return isRow(this) ? 300 : 0; } });
    Object.defineProperty(Element.prototype, 'scrollLeft', { configurable: true, get() { return left.get(this) ?? 0; }, set(v: number) { left.set(this, v); } });
    Element.prototype.getBoundingClientRect = function (this: Element) {
      const row = this.parentElement;
      const [l, r] = isRow(this) ? [0, 300] : row && isRow(row) ? [[...row.children].indexOf(this) * 150 - (left.get(row) ?? 0), 0] : [0, 0];
      const right = isRow(this) ? r : l + 100;
      return { left: l, right, top: 0, bottom: 0, width: right - l, height: 0, x: l, y: 0, toJSON: () => ({}) } as DOMRect;
    };
    try {
      const tab = (label: string) => ({ label, href: `/activity/${label.toLowerCase()}` });
      const { container, rerender } = render(Tabs, { props: { tabs: ['Tasks', 'Audit log'].map(tab), current: '/activity/audit log' } });
      expect(container.querySelector('.tabs')!.scrollLeft).toBe(0);
      await rerender({ tabs: ['Tasks', 'Schedules', 'Releases', 'Audit log'].map(tab), current: '/activity/audit log' });
      expect(container.querySelector('.tabs')!.scrollLeft).toBe(250);
    } finally {
      Element.prototype.getBoundingClientRect = saved.rect;
      for (const k of ['scrollWidth', 'clientWidth', 'scrollLeft'] as const) {
        const d = (saved as Record<string, PropertyDescriptor | undefined>)[k];
        if (d) Object.defineProperty(Element.prototype, k, d);
        else delete (Element.prototype as unknown as Record<string, unknown>)[k];
      }
    }
  });
});
