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

export class EventStream {
  status: 'idle' | 'connecting' | 'open' | 'error' = $state('idle');
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

  connect(url = '/api/events') {
    if (this.#es || this.#timer) return;
    this.#url = url;
    this.#open();
  }

  #open() {
    this.#timer = undefined;
    this.status = 'connecting';
    // The browser resends Last-Event-ID on its own reconnects; the server
    // replays from its ring or answers "resync".
    const es = new EventSource(this.#url);
    this.#es = es;
    es.onopen = () => {
      this.status = 'open';
      this.#retry = 0;
    };
    es.onerror = () => {
      if (es.readyState !== EventSource.CLOSED) {
        this.status = 'connecting';
        return;
      }
      // A CLOSED source (HTTP error, auth expiry) never retries by itself, and
      // a fresh one carries no Last-Event-ID, so whatever happened meanwhile
      // must be refetched once the new stream says hello.
      es.close();
      this.#es = null;
      this.status = 'error';
      this.#missed = true;
      const delay = Math.min(maxBackoff, 1000 * 2 ** this.#retry++);
      this.#timer = setTimeout(() => this.#open(), delay);
      this.#closed.forEach((fn) => fn());
    };
    es.addEventListener('hello', (m) => {
      let h: Hello;
      try {
        h = JSON.parse((m as MessageEvent).data) as Hello;
      } catch {
        return;
      }
      const rebooted = this.#boot !== '' && this.#boot !== h.boot;
      this.#boot = h.boot;
      this.hello = h;
      this.#hello.forEach((fn) => fn(h));
      if (rebooted || this.#missed) {
        this.#missed = false;
        this.#queueResync();
      }
    });
    es.addEventListener('resync', () => this.#queueResync());
    for (const t of eventTypes) {
      es.addEventListener(t, (m) => {
        let ev: BusEvent;
        try {
          ev = JSON.parse((m as MessageEvent).data) as BusEvent;
        } catch {
          return;
        }
        this.#handlers.get(t)?.forEach((fn) => fn(ev));
        this.#handlers.get('*')?.forEach((fn) => fn(ev));
      });
    }
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
