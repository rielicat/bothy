import { render, screen } from '@testing-library/svelte';
import { describe, expect, it, vi } from 'vitest';
import ScreenLoading from './ScreenLoading.svelte';

describe('a screen still loading (§13.2)', () => {
  it('says Loading… only after 400 ms', async () => {
    vi.useFakeTimers();
    try {
      render(ScreenLoading);
      const p = screen.getByText('Loading…');
      expect(p.classList.contains('invisible')).toBe(true);
      await vi.advanceTimersByTimeAsync(400);
      expect(p.classList.contains('invisible')).toBe(false);
      // The words come with the spinner, the one looping mark (§3.6).
      expect(p.querySelector('use')?.getAttribute('href')).toBe('#i-spinner');
    } finally {
      vi.useRealTimers();
    }
  });

  it('offers Try again when the chunk did not arrive', () => {
    render(ScreenLoading, { failed: true });
    expect(screen.getByText("This page didn't load. Check your connection and try again.")).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Try again' })).toBeTruthy();
  });
});
