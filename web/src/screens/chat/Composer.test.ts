import { fireEvent, render, screen } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import Composer from './Composer.svelte';

const props = { ensureSession: async () => 'ses_1', onsend: async () => true, oncommand: () => {}, onstop: () => {} };

describe('the composer at rest (§7.5)', () => {
  const box = () => screen.getByRole('textbox', { name: 'Message' }).parentElement!;

  it('keeps a calm border when it takes focus by itself or from a click', async () => {
    render(Composer, props);
    const field = screen.getByRole('textbox', { name: 'Message' });
    await fireEvent.pointerDown(document.body);
    field.focus();
    // No halo in any form, not even one keyed to the field's focus: it would be on whenever the composer is.
    expect(box().className).not.toMatch(/shadow-focus/);
    expect(box().className).toContain('has-[textarea:focus]:border-focus');
  });

  it('adds the halo when the keyboard brought focus, and drops it on blur', async () => {
    render(Composer, props);
    const field = screen.getByRole('textbox', { name: 'Message' });
    await fireEvent.keyDown(document.body, { key: 'Tab' });
    field.focus();
    await Promise.resolve();
    expect(box().classList.contains('shadow-focus')).toBe(true);
    field.blur();
    await Promise.resolve();
    expect(box().classList.contains('shadow-focus')).toBe(false);
  });

  it('keeps the halo off while typing after a click', async () => {
    render(Composer, props);
    const field = screen.getByRole('textbox', { name: 'Message' });
    await fireEvent.pointerDown(field);
    field.focus();
    await fireEvent.keyDown(field, { key: 'a' });
    await Promise.resolve();
    expect(box().className).not.toMatch(/shadow-focus/);
  });
});
