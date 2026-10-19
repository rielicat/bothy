// The lines under "What Bothy did" on a task (docs/design/ui.md §10.2), in
// the chat's own words for each tool (lib/activity.ts, §7.4).
import { fill, lineFor, phrases, prefix, type Phrase } from '../../lib/activity';
import type { Approval, ToolCallRecord } from '../../lib/api/types.gen';
import { approvalsIn, withdrawn } from '../../lib/cardcopy';

export type Segment = { text: string; code: boolean };

// Names people chose stay in the body font; paths, hosts and commands go to text-code.
// A repository before a path inside it reads "acme/web · ", as in the chat.
const segments = (p: Phrase): Segment[] => p.map((x) => ({ text: x.k === 'repo' ? `${x.v} · ` : x.v, code: x.k === 'code' }));

export type StepState = 'running' | 'waiting' | 'done' | 'failed' | 'denied';

export function stepState(status: string): StepState {
  if (status === 'ok') return 'done';
  if (status === 'planned' || status === 'waiting') return 'waiting';
  if (status === 'approved' || status === 'running') return 'running';
  if (status === 'denied' || status === 'stopped') return 'denied';
  return 'failed';
}

export interface StepLine {
  key: string;
  state: StepState;
  segments: Segment[];
  call?: ToolCallRecord;
  approval?: Approval;
}

/** The line for one tool call; the tense follows the state, never ahead of the result. */
export function stepLine(t: ToolCallRecord): StepLine {
  const state = stepState(t.status);
  return { key: t.id, state, segments: segments(lineFor(phrases({ tool: t.tool, action: t.action, result: t.result_preview }), state)), call: t };
}

/** "Maya Chen approved opening a pull request in acme/web". */
export function decisionLine(a: Approval, who: string): StepLine | null {
  if (!a.action) return null;
  const p = phrases({ tool: a.action.tool, action: a.action });
  const line =
    a.status === 'allowed'
      ? prefix(`${who} approved `, p.ing)
      : withdrawn(a)
        ? fill('Cancelled by {who}', { who: who === 'You' ? 'you' : who })
        : a.status === 'denied'
          ? prefix(`${who} denied `, p.ing)
          : a.status === 'expired'
            ? prefix(`${approvalsIn(a) ? `Only ${approvalsIn(a)} of ${a.approver?.required} approvals came in` : 'Nobody answered'}, so Bothy didn't `, p.ask)
            : a.status === 'cancelled'
              ? prefix("Cancelled: Bothy didn't ", p.ask)
              : null;
  return line ? { key: a.id, state: a.status === 'allowed' ? 'done' : 'denied', segments: segments(line), approval: a } : null;
}

/** "How it was allowed" in a step's Details. */
export function allowedHow(t: ToolCallRecord, approval: Approval | undefined, who: string): string {
  if (approval?.status === 'allowed') return who === 'You' ? 'You approved it' : `${who} approved it`;
  if (t.rule.startsWith('grant:')) return 'Allowed by a saved permission';
  if (t.verdict === 'auto') return 'Allowed automatically';
  if (t.verdict === 'deny') return 'Blocked by a rule';
  return '';
}
