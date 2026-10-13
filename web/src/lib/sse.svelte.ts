import type { BannerData, Event as BusEvent, EventType } from './api/types.gen';

export interface Hello {
  boot: string;
  seq: number;
  paused: boolean;
  sandbox: boolean;
  banners: BannerData[];
}

type Handler = (e: BusEvent) => void;

// SPEC §8.2 event names; EventSource only dispatches named events that have a listener.
export const eventTypes: EventType[] = [
  'run.queued',
  'run.started',
  'run.status',
  'run.delta',
  'run.thinking',
  'run.tool_call',
  'run.tool_output',
  'run.tool_result',
  'run.usage',
  'run.parked',
  'run.finished',
  'approval.requested',
  'approval.resolved',
  'message.created',
  'session.updated',
  'memory.updated',
  'egress.blocked',
  'schedule.run',
  'budget.warning',
  'pairing.linked',
  'channel.status',
  'sandbox.status',
  'mcp.pin_pending',
  'config.changed',
  'banner',
];

const maxBackoff = 30_000;

// Tabs share one stream. The server allows four per user and closes the
// oldest, so a fifth tab would start an eviction loop, and every stream holds
// one of the browser's six connections to the gateway. The tab holding the
// Web Lock opens the EventSource and relays every frame over a
// BroadcastChannel; when it goes away the lock passes to another tab, which
// reconnects and tells the others to refetch what the handoff missed.
const shareName = 'bothy-events';

type Status = 'idle' | 'connecting' | 'open' | 'error';

type Relay = { k: 'frame'; t: string; d: string } | { k: 'status'; s: Status } | { k: 'closed' } | { k: 'join' };

function canShare(): boolean {
  return typeof BroadcastChannel !== 'undefined' && typeof navigator !== 'undefined' && typeof navigator.locks?.request === 'function';
}

export class EventStream {
  status: Status = $state('idle');
  hello: Hello | null = $state(null);

  #url = '/api/events';
  #es: EventSource | null = null;
  #handlers = new Map<string, Set<Handler>>();
  #resync = new Set<() => void>();
  #hello = new Set<(h: Hello) => void>();
  #closed = new Set<() => void>();
  #boot = '';
  #missed = false;
  #retry = 0;
  #timer: ReturnType<typeof setTimeout> | undefined;
  #resyncQueued = false;

  #bc: BroadcastChannel | null = null;
  #abort: AbortController | null = null;
  #unlock: (() => void) | null = null;
  #leader = false;
  #followed = false;
  #lastHello = '';

  connect(url = '/api/events') {
    if (this.#es || this.#timer || this.#bc) return;
    this.#url = url;
    if (!canShare()) {
      this.#open();
      return;
    }
    const bc = new BroadcastChannel(shareName);
    this.#bc = bc;
    bc.onmessage = (m: MessageEvent) => this.#relayed(m.data as Relay);
    const abort = new AbortController();
    this.#abort = abort;
    this.status = 'connecting';
    navigator.locks
      .request(shareName, { signal: abort.signal }, () => {
        if (abort.signal.aborted) return;
        this.#leader = true;
        // Frames the previous leader had not relayed yet are lost.
        this.#missed = this.#missed || this.#followed;
        this.#open();
        return new Promise<void>((resolve) => (this.#unlock = resolve));
      })
      .catch(() => {});
    bc.postMessage({ k: 'join' } satisfies Relay);
  }

  #post(r: Relay) {
    if (this.#leader) this.#bc?.postMessage(r);
  }

  #setStatus(s: Status) {
    this.status = s;
    this.#post({ k: 'status', s });
  }

  #relayed(r: Relay) {
    if (this.#leader) {
      if (r.k === 'join') {
        this.#post({ k: 'status', s: this.status });
        if (this.#lastHello) this.#post({ k: 'frame', t: 'hello', d: this.#lastHello });
      }
      return;
    }
    switch (r.k) {
      case 'frame':
        this.#followed = true;
        this.#frame(r.t, r.d);
        break;
      case 'status':
        this.status = r.s;
        break;
      case 'closed':
        this.#closed.forEach((fn) => fn());
        break;
    }
  }

  #open() {
    this.#timer = undefined;
    this.#setStatus('connecting');
    // The browser resends Last-Event-ID on its own reconnects; the server
    // replays from its ring or answers "resync".
    const es = new EventSource(this.#url);
    this.#es = es;
    es.onopen = () => {
      this.#setStatus('open');
      this.#retry = 0;
    };
    es.onerror = () => {
      if (es.readyState !== EventSource.CLOSED) {
        this.#setStatus('connecting');
        return;
      }
      // A CLOSED source (HTTP error, auth expiry) never retries by itself, and
      // a fresh one carries no Last-Event-ID, so whatever happened meanwhile
      // must be refetched once the new stream says hello.
      es.close();
      this.#es = null;
      this.#setStatus('error');
      this.#missed = true;
      const delay = Math.min(maxBackoff, 1000 * 2 ** this.#retry++);
      this.#timer = setTimeout(() => this.#open(), delay);
      this.#closed.forEach((fn) => fn());
      this.#post({ k: 'closed' });
    };
    for (const t of ['hello', 'resync', ...eventTypes]) {
      es.addEventListener(t, (m) => {
        const d = (m as MessageEvent).data as string;
        this.#frame(t, d);
        this.#post({ k: 'frame', t, d });
      });
    }
  }

  #frame(t: string, d: string) {
    if (t === 'resync') {
      this.#queueResync();
      return;
    }
    if (t === 'hello') {
      let h: Hello;
      try {
        h = JSON.parse(d) as Hello;
      } catch {
        return;
      }
      const rebooted = this.#boot !== '' && this.#boot !== h.boot;
      this.#boot = h.boot;
      this.#lastHello = d;
      this.hello = h;
      this.#hello.forEach((fn) => fn(h));
      if (rebooted || this.#missed) {
        // Other tabs saw the same boot, so only a gap needs telling them.
        if (this.#missed) this.#post({ k: 'frame', t: 'resync', d: '{}' });
        this.#missed = false;
        this.#queueResync();
      }
      return;
    }
    let ev: BusEvent;
    try {
      ev = JSON.parse(d) as BusEvent;
    } catch {
      return;
    }
    this.#handlers.get(t)?.forEach((fn) => fn(ev));
    this.#handlers.get('*')?.forEach((fn) => fn(ev));
  }

  // A reboot shows up both as a new hello boot and as a resync frame; one
  // refetch is enough.
  #queueResync() {
    if (this.#resyncQueued) return;
    this.#resyncQueued = true;
    setTimeout(() => {
      this.#resyncQueued = false;
      this.#resync.forEach((fn) => fn());
    }, 0);
  }

  close() {
    clearTimeout(this.#timer);
    this.#timer = undefined;
    this.#es?.close();
    this.#es = null;
    this.#abort?.abort();
    this.#abort = null;
    this.#unlock?.();
    this.#unlock = null;
    this.#bc?.close();
    this.#bc = null;
    this.#leader = false;
    this.#followed = false;
    this.#lastHello = '';
    this.#boot = '';
    this.#missed = false;
    this.#retry = 0;
    this.hello = null;
    this.status = 'idle';
  }

  on(type: EventType | '*', fn: Handler): () => void {
    let set = this.#handlers.get(type);
    if (!set) this.#handlers.set(type, (set = new Set()));
    set.add(fn);
    return () => set.delete(fn);
  }

  // fn refetches sessions, active runs and approvals over REST.
  onResync(fn: () => void): () => void {
    this.#resync.add(fn);
    return () => this.#resync.delete(fn);
  }

  onHello(fn: (h: Hello) => void): () => void {
    this.#hello.add(fn);
    return () => this.#hello.delete(fn);
  }

  // fn runs when the server ends the stream for good (an HTTP error); the
  // stream itself keeps retrying with backoff until close().
  onClosed(fn: () => void): () => void {
    this.#closed.add(fn);
    return () => this.#closed.delete(fn);
  }
}

export const events = new EventStream();
