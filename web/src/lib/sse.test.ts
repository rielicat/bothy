import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { FakeEventSource } from '../test/setup';
import { EventStream } from './sse.svelte';

const hello = (boot: string) => ({ boot, seq: 1, paused: false, sandbox: true, banners: [] });
const ev = (type: string, seq: number) => ({ seq, type, principal_id: 'usr_a', session_id: 'ses_a', at: '2026-10-12T00:00:00Z', data: { text: 'hi' } });

describe('EventStream', () => {
  let s: EventStream;

  beforeEach(() => {
    vi.useFakeTimers();
    FakeEventSource.instances = [];
    s = new EventStream();
  });

  afterEach(() => {
    s.close();
    vi.useRealTimers();
  });

  it('opens one stream, records hello and dispatches typed and wildcard handlers', () => {
    s.connect();
    s.connect();
    expect(FakeEventSource.instances).toHaveLength(1);
    const es = FakeEventSource.last;
    expect(es.url).toBe('/api/events');
    es.open();
    expect(s.status).toBe('open');
    es.emit('hello', hello('b1'));
    expect(s.hello?.boot).toBe('b1');

    const typed = vi.fn();
    const any = vi.fn();
    const off = s.on('run.delta', typed);
    s.on('*', any);
    es.emit('run.delta', ev('run.delta', 2));
    es.emit('message.created', ev('message.created', 3));
    expect(typed).toHaveBeenCalledTimes(1);
    expect(typed.mock.calls[0][0].seq).toBe(2);
    expect(any).toHaveBeenCalledTimes(2);

    off();
    es.emit('run.delta', ev('run.delta', 4));
    expect(typed).toHaveBeenCalledTimes(1);
  });

  it('ignores frames that are not JSON', () => {
    s.connect();
    const fn = vi.fn();
    s.on('*', fn);
    FakeEventSource.last.emit('run.delta', '{not json');
    expect(fn).not.toHaveBeenCalled();
  });

  it('refetches on a resync frame, once even if the boot changed too', () => {
    const resync = vi.fn();
    s.onResync(resync);
    s.connect();
    const es = FakeEventSource.last;
    es.emit('hello', hello('b1'));
    vi.runAllTimers();
    expect(resync).not.toHaveBeenCalled();

    // A gateway restart: the browser reconnects by itself, gets a new hello and a resync.
    es.fail(false);
    expect(s.status).toBe('connecting');
    es.emit('hello', hello('b2'));
    es.emit('resync', {});
    vi.runAllTimers();
    expect(resync).toHaveBeenCalledTimes(1);
  });

  it('reconnects a closed stream with backoff and resyncs after the new hello', () => {
    const resync = vi.fn();
    s.onResync(resync);
    s.connect();
    const first = FakeEventSource.last;
    first.emit('hello', hello('b1'));

    first.fail(true);
    expect(s.status).toBe('error');
    expect(FakeEventSource.instances).toHaveLength(1);
    vi.advanceTimersByTime(999);
    expect(FakeEventSource.instances).toHaveLength(1);
    vi.advanceTimersByTime(1);
    expect(FakeEventSource.instances).toHaveLength(2);

    // second failure doubles the delay
    FakeEventSource.last.fail(true);
    vi.advanceTimersByTime(1999);
    expect(FakeEventSource.instances).toHaveLength(2);
    vi.advanceTimersByTime(1);
    const third = FakeEventSource.last;
    expect(FakeEventSource.instances).toHaveLength(3);

    third.open();
    // same boot: only the missed window forces the refetch
    third.emit('hello', hello('b1'));
    vi.runAllTimers();
    expect(resync).toHaveBeenCalledTimes(1);
  });

  it('notifies hello listeners and stops everything on close', () => {
    const onHello = vi.fn();
    s.onHello(onHello);
    s.connect();
    FakeEventSource.last.emit('hello', hello('b1'));
    expect(onHello).toHaveBeenCalledWith(expect.objectContaining({ boot: 'b1' }));

    FakeEventSource.last.fail(true);
    s.close();
    vi.runAllTimers();
    expect(FakeEventSource.instances).toHaveLength(1);
    expect(s.status).toBe('idle');
    expect(s.hello).toBeNull();
  });

  it('tells closed listeners when the server refuses the stream, not on transient drops', () => {
    const closed = vi.fn();
    s.onClosed(closed);
    s.connect();
    FakeEventSource.last.emit('hello', 'not json');
    expect(s.hello).toBeNull();
    FakeEventSource.last.fail(false);
    expect(closed).not.toHaveBeenCalled();
    FakeEventSource.last.fail(true);
    expect(closed).toHaveBeenCalledTimes(1);
  });
});

