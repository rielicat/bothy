import { render, screen } from '@testing-library/svelte';
import { flushSync } from 'svelte';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { events } from '../../lib/sse.svelte';
import ConnectionStatus from './ConnectionStatus.svelte';

const set = (s: typeof events.status) => {
  events.status = s;
  flushSync();
};

describe('connection line (§13.2)', () => {
  afterEach(() => {
    vi.useRealTimers();
    events.status = 'idle';
  });

  it('says nothing on the first connection, then follows a lost one', () => {
    vi.useFakeTimers();
    render(ConnectionStatus);
    set('connecting');
    set('open');
    expect(document.body.textContent).toBe('');

    set('connecting');
    expect(screen.getByText('Reconnecting…')).toBeTruthy();
    vi.advanceTimersByTime(30_000);
    flushSync();
    expect(screen.getByText("You're offline. This page will update when you're back.")).toBeTruthy();

    set('open');
    expect(screen.getByText('Connected')).toBeTruthy();
    vi.advanceTimersByTime(2000);
    flushSync();
    expect(screen.queryByText('Connected')).toBeNull();
  });

  it('goes quiet when the stream is closed on sign-out', () => {
    render(ConnectionStatus);
    set('open');
    set('error');
    expect(screen.getByText('Reconnecting…')).toBeTruthy();
    set('idle');
    expect(screen.queryByText('Reconnecting…')).toBeNull();
  });
});
