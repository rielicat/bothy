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
const eventTypes: EventType[] = [
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

class EventStream {
  status: 'idle' | 'connecting' | 'open' | 'error' = $state('idle');
  hello: Hello | null = $state(null);

  #es: EventSource | null = null;
  #handlers = new Map<string, Set<Handler>>();
  #resync = new Set<() => void>();

  connect(url = '/api/events') {
    if (this.#es) return;
    this.status = 'connecting';
    // The browser resends Last-Event-ID on reconnect; the server replays or answers resync.
    const es = new EventSource(url);
    this.#es = es;
    es.onopen = () => (this.status = 'open');
    es.onerror = () => (this.status = es.readyState === EventSource.CLOSED ? 'error' : 'connecting');
    es.addEventListener('hello', (m) => (this.hello = JSON.parse((m as MessageEvent).data) as Hello));
    es.addEventListener('resync', () => this.#resync.forEach((fn) => fn()));
    for (const t of eventTypes) {
      es.addEventListener(t, (m) => {
        const ev = JSON.parse((m as MessageEvent).data) as BusEvent;
        this.#handlers.get(t)?.forEach((fn) => fn(ev));
        this.#handlers.get('*')?.forEach((fn) => fn(ev));
      });
    }
  }

  close() {
    this.#es?.close();
    this.#es = null;
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
}

export const events = new EventStream();
