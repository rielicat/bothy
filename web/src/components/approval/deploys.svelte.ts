import { api } from '../../lib/api/client';
import type { Event as BusEvent } from '../../lib/api/types.gen';
import { events } from '../../lib/sse.svelte';
import type { Deploy } from './types';

// One read of the deploy list serves every deploy line on screen (§8.7), so a
// chat with several approved deploys asks once. It reads again on release
// events and, while a deploy it shows is running, every 15 s: the events reach
// only the deploy's requester and admins, not its other approvers.
class Deploys {
  list = $state<Deploy[]>([]);
  #lines = new Map<string, number>();
  #heard = new Set<string>();
  #queued = false;
  #reading = false;
  #again = false;
  #timer: ReturnType<typeof setTimeout> | undefined;
  #offs: (() => void)[] = [];
  #gen = 0;

  of(approval: string): Deploy | null {
    return this.list.find((d) => d.approval_id === approval) ?? null;
  }

  watch(approval: string): () => void {
    if (!this.#lines.size) {
      const moved = (e: BusEvent) => {
        // An admin who asked gets two copies of each event (SPEC §8.2).
        const key = `${e.type}:${(e.data as { deploy_id?: string } | undefined)?.deploy_id}`;
        if (this.#heard.has(key)) return;
        this.#heard.add(key);
        void this.read();
      };
      this.#offs = [events.on('release.dispatched', moved), events.on('release.finished', moved), events.onResync(() => void this.read())];
    }
    this.#lines.set(approval, (this.#lines.get(approval) ?? 0) + 1);
    // Lines that mount together, as when a chat opens, share one read.
    if (!this.#queued) {
      this.#queued = true;
      void Promise.resolve().then(() => {
        this.#queued = false;
        void this.read();
      });
    }
    return () => {
      const n = (this.#lines.get(approval) ?? 1) - 1;
      if (n) this.#lines.set(approval, n);
      else this.#lines.delete(approval);
      if (this.#lines.size) return;
      this.#offs.forEach((off) => off());
      clearTimeout(this.#timer);
      // The next screen may be another person's, after a sign-out: it starts
      // from its own read, never from this one's deploys.
      this.list = [];
      this.#heard.clear();
      this.#gen++;
    };
  }

  async read(): Promise<void> {
    if (this.#reading) {
      // The change that asked for this may have landed after the answer in flight.
      this.#again = true;
      return;
    }
    if (!this.#lines.size) return;
    clearTimeout(this.#timer);
    this.#reading = true;
    const gen = this.#gen;
    try {
      const r = await api.get<{ deploys?: (Deploy | null)[] }>('/api/releases');
      if (gen === this.#gen) this.list = (r.deploys ?? []).filter((d): d is Deploy => !!d);
    } catch {
      // Keeps the last answer; without the releases API the lines show nothing.
    } finally {
      this.#reading = false;
    }
    if (this.#again) {
      this.#again = false;
      return this.read();
    }
    // unknown is final: Bothy stopped following the run, so asking again changes nothing.
    const open = [...this.#lines.keys()].some((id) => ['dispatched', 'running'].includes(this.of(id)?.status ?? ''));
    if (open) this.#timer = setTimeout(() => void this.read(), 15_000);
  }
}

export const deploys = new Deploys();
