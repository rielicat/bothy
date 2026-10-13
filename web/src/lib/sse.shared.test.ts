import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { FakeEventSource } from '../test/setup';
import { EventStream } from './sse.svelte';

// Exclusive locks by name, each granted in request order, like navigator.locks.
class FakeLocks {
  #locks = new Map<string, { held: boolean; queue: (() => void)[] }>();

  request(name: string, opts: { signal?: AbortSignal }, cb: () => unknown): Promise<unknown> {
    let l = this.#locks.get(name);
    if (!l) this.#locks.set(name, (l = { held: false, queue: [] }));
    const lock = l;
    const next = () => {
      const n = lock.queue.shift();
      if (n) queueMicrotask(n);
      else lock.held = false;
    };
    return new Promise((resolve, reject) => {
      const run = () => {
        if (opts.signal?.aborted) {
          reject(new DOMException('aborted', 'AbortError'));
          next();
          return;
        }
        void Promise.resolve(cb()).then((v) => {
          resolve(v);
          next();
        });
      };
      opts.signal?.addEventListener('abort', () => {
        const i = lock.queue.indexOf(run);
        if (i >= 0) {
          lock.queue.splice(i, 1);
          reject(new DOMException('aborted', 'AbortError'));
        }
      });
      if (lock.held) {
        lock.queue.push(run);
      } else {
        lock.held = true;
        queueMicrotask(run);
      }
    });
  }
}

class FakeBroadcastChannel {
  static open = new Set<FakeBroadcastChannel>();
  onmessage: ((m: MessageEvent) => void) | null = null;

  constructor(readonly name: string) {
    FakeBroadcastChannel.open.add(this);
  }

  postMessage(data: unknown) {
    const copy = structuredClone(data);
    for (const c of FakeBroadcastChannel.open) {
      if (c === this || c.name !== this.name) continue;
      queueMicrotask(() => {
        if (FakeBroadcastChannel.open.has(c)) c.onmessage?.(new MessageEvent('message', { data: copy }));
      });
    }
  }

  close() {
    FakeBroadcastChannel.open.delete(this);
  }
}

const hello = (boot: string) => ({ boot, seq: 1, paused: false, sandbox: true, banners: [] });
const ev = (seq: number) => ({ seq, type: 'run.delta', principal_id: 'usr_a', session_id: 'ses_a', at: '2026-10-12T00:00:00Z', data: { text: 'hi' } });

async function settle() {
  for (let i = 0; i < 20; i++) await Promise.resolve();
}

describe('EventStream shared across tabs', () => {
  let tabs: EventStream[];

  beforeEach(() => {
    vi.useFakeTimers();
    FakeEventSource.instances = [];
    FakeBroadcastChannel.open.clear();
    vi.stubGlobal('BroadcastChannel', FakeBroadcastChannel);
    Object.defineProperty(navigator, 'locks', { value: new FakeLocks(), configurable: true });
    tabs = [];
  });

  afterEach(() => {
    tabs.forEach((t) => t.close());
    delete (navigator as unknown as { locks?: unknown }).locks;
    vi.unstubAllGlobals();
    vi.useRealTimers();
  });

  const open = async (n: number, user = 'usr_a') => {
    for (let i = 0; i < n; i++) {
      const t = new EventStream();
      tabs.push(t);
      t.connect(user);
    }
    await settle();
  };

  it('five tabs open one stream and all see its frames and status', async () => {
    await open(5);
    expect(FakeEventSource.instances).toHaveLength(1);
    const es = FakeEventSource.last;
    const seen = tabs.map(() => vi.fn());
    tabs.forEach((t, i) => t.on('run.delta', seen[i]));
    es.open();
    es.emit('hello', hello('b1'));
    es.emit('run.delta', ev(2));
    await settle();
    for (const [i, t] of tabs.entries()) {
      expect(seen[i]).toHaveBeenCalledTimes(1);
      expect(t.status).toBe('open');
      expect(t.hello?.boot).toBe('b1');
    }
  });

  it('a tab opened later gets the current hello without a refetch', async () => {
    await open(1);
    FakeEventSource.last.open();
    FakeEventSource.last.emit('hello', hello('b1'));
    const late = new EventStream();
    tabs.push(late);
    const onHello = vi.fn();
    const resync = vi.fn();
    late.onHello(onHello);
    late.onResync(resync);
    late.connect('usr_a');
    await settle();
    vi.runAllTimers();
    expect(FakeEventSource.instances).toHaveLength(1);
    expect(onHello).toHaveBeenCalledWith(expect.objectContaining({ boot: 'b1' }));
    expect(late.status).toBe('open');
    expect(resync).not.toHaveBeenCalled();
  });

  it('when the streaming tab closes another takes over and every tab refetches once', async () => {
    await open(3);
    const first = FakeEventSource.last;
    first.open();
    first.emit('hello', hello('b1'));
    await settle();
    const resyncs = tabs.map(() => vi.fn());
    tabs.forEach((t, i) => t.onResync(resyncs[i]));

    tabs[0].close();
    await settle();
    expect(first.readyState).toBe(FakeEventSource.CLOSED);
    expect(FakeEventSource.instances).toHaveLength(2);
    const second = FakeEventSource.last;
    second.open();
    second.emit('hello', hello('b1'));
    await settle();
    vi.runAllTimers();
    expect(resyncs[0]).not.toHaveBeenCalled();
    expect(resyncs[1]).toHaveBeenCalledTimes(1);
    expect(resyncs[2]).toHaveBeenCalledTimes(1);

    const seen = vi.fn();
    tabs[2].on('run.delta', seen);
    second.emit('run.delta', ev(3));
    await settle();
    expect(seen).toHaveBeenCalledTimes(1);
  });

  it('tells every tab when the server refuses the stream', async () => {
    await open(2);
    const closed = tabs.map(() => vi.fn());
    tabs.forEach((t, i) => t.onClosed(closed[i]));
    FakeEventSource.last.fail(true);
    await settle();
    expect(closed[0]).toHaveBeenCalledTimes(1);
    expect(closed[1]).toHaveBeenCalledTimes(1);
    expect(tabs[1].status).toBe('error');
  });

  // A shared computer: one person's tabs must never feed another's.
  it('tabs signed in as different users never share a stream', async () => {
    await open(1, 'usr_a');
    const a = FakeEventSource.last;
    a.open();
    a.emit('hello', hello('b1'));
    await open(1, 'usr_b');
    const b = tabs[1];
    const seen = vi.fn();
    const hellos = vi.fn();
    b.on('*', seen);
    b.onHello(hellos);
    a.emit('approval.requested', { ...ev(2), type: 'approval.requested' });
    await settle();
    expect(FakeEventSource.instances).toHaveLength(2);
    expect(seen).not.toHaveBeenCalled();
    expect(hellos).not.toHaveBeenCalled();
  });

  it('logging out in one tab ends the shared stream in every tab', async () => {
    await open(3);
    const es = FakeEventSource.last;
    es.open();
    es.emit('hello', hello('b1'));
    await settle();
    const out = tabs.map(() => vi.fn());
    tabs.forEach((t, i) => t.onLogout(out[i]));
    tabs[2].logout();
    await settle();
    expect(es.readyState).toBe(FakeEventSource.CLOSED);
    expect(out[0]).toHaveBeenCalledTimes(1);
    expect(out[1]).toHaveBeenCalledTimes(1);
    expect(out[2]).not.toHaveBeenCalled();
    vi.runAllTimers();
    await settle();
    expect(FakeEventSource.instances).toHaveLength(1);
    expect(tabs.map((t) => t.status)).toEqual(['idle', 'idle', 'idle']);
  });

  it('a new tab gets the current state, and the other tabs keep theirs', async () => {
    await open(2);
    const es = FakeEventSource.last;
    es.open();
    es.emit('hello', { ...hello('b1'), banners: [{ level: 'error', code: 'vault_sealed', text: 'The vault is sealed.' }] });
    await settle();
    const banner = (seq: number, data: object) => ({ seq, type: 'banner', at: '2026-10-12T00:00:00Z', data });
    es.emit('banner', banner(2, { level: 'error', code: 'vault_sealed', text: '', clear: true }));
    es.emit('banner', banner(3, { level: 'error', code: 'paused', text: 'Bothy is paused by an admin.' }));
    es.emit('sandbox.status', { seq: 4, type: 'sandbox.status', at: '2026-10-12T00:00:00Z', data: { status: { available: false } } });
    await settle();
    const before = tabs.map(() => vi.fn());
    tabs.forEach((t, i) => t.onHello(before[i]));

    await open(1);
    const late = tabs[2];
    expect(late.hello).toMatchObject({ boot: 'b1', paused: true, sandbox: false, banners: [{ code: 'paused' }] });
    expect(before[0]).not.toHaveBeenCalled();
    expect(before[1]).not.toHaveBeenCalled();
  });
});
