// Vote state of multi-approver approvals ("1 of 2 approvals", S4), as the
// viewer sees it. The event stream does not carry approval.voted to the web
// yet, so open cards re-read it every 15 s and after each decision.
import { api } from '../../lib/api/client';
import type { ApprovalVotes } from '../../lib/api/types.gen';

class Votes {
  /** null: the server has no vote state for this approval (single approver, or before S4). */
  byId: Record<string, ApprovalVotes | null> = $state({});
  #inflight = new Map<string, Promise<void>>();

  load(id: string): Promise<void> {
    const running = this.#inflight.get(id);
    if (running) return running;
    const p = api
      .get<ApprovalVotes>(`/api/approvals/${encodeURIComponent(id)}/votes`)
      .then(
        (v) => void (this.byId[id] = v),
        () => void (this.byId[id] = this.byId[id] ?? null),
      )
      .finally(() => this.#inflight.delete(id));
    this.#inflight.set(id, p);
    return p;
  }

  reset() {
    this.byId = {};
  }
}

export const votes = new Votes();
