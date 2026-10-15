// A transcript tool item with everything its activity line needs: the words,
// the state (from the live run, the stored record and the approval) and the
// approval itself.
import { phrases, stepLink, stepState, type Phrases, type StepInput, type StepState } from '../../lib/activity';
import type { Approval, ToolCallRecord } from '../../lib/api/types.gen';
import type { ToolItem } from '../../lib/transcript';

export interface StepView {
  key: string;
  item: ToolItem;
  input: StepInput;
  phrases: Phrases;
  state: StepState;
  approval?: Approval;
  record?: ToolCallRecord;
  link: string;
  /** Why Bothy didn't do the step, in plain words: "A deploy to production needs 2 approvers…". */
  why: string;
}

// Reasons stored before the server wrote them in plain words name tools,
// rules, run kinds or Go errors; they get one plain sentence instead.
const technical = [
  /actions are denied by the policy defaults$/,
  /is not available in .+ runs$/,
  /needs the \w+ role$/,
  /restricted spaces/,
  /^the tool call could not be checked/,
  /^self-modification /,
  /^memory writes are off /,
];

/**
 * Why "Denied by policy (<rule>): <reason>" refused a step, as a sentence: the
 * server says it in plain words (an admin writes a rule's own reason). The
 * full result stays in the step's Details.
 */
export function refusalReason(result: string | undefined): string {
  const m = /^Denied by policy \(([^)]*)\): (.+)$/s.exec(result?.trim() ?? '');
  if (!m) return '';
  const rule = m[1];
  const reason = m[2].trim();
  if (rule === 'floor:paused' && /^Bothy is paused by an admin/.test(reason)) return 'Bothy is paused.';
  if (rule === 'floor:release') {
    if (/^deploys are not allowed in /.test(reason)) return 'Deploys can only be asked for by a person in a chat.';
    const env = /^this deploy does not match the (.+) environment: /.exec(reason);
    if (env) return `This deploy doesn't match how ${env[1]} is set up.`;
  }
  if (technical.some((re) => re.test(reason))) return "Bothy's rules don't allow this.";
  return reason.charAt(0).toUpperCase() + reason.slice(1) + (/[.!?]$/.test(reason) ? '' : '.');
}

export interface StepContext {
  byCall: Map<string, Approval>;
  records: Map<string, ToolCallRecord>;
  /** The run that is still going, if any. */
  liveRun: string;
}

export function stepOf(t: ToolItem, ctx: StepContext): StepView {
  const record = t.call && t.runId ? ctx.records.get(`${t.runId}:${t.call.id}`) : undefined;
  const tcl = t.live?.callId ?? record?.id;
  const approval = (tcl && ctx.byCall.get(tcl)) || undefined;
  const result = t.result?.content ?? t.live?.preview ?? record?.result_preview;
  const input: StepInput = {
    tool: t.tool,
    action: t.live?.action ?? record?.action,
    args: t.args,
    title: t.tool.startsWith('mcp__') ? t.live?.title : undefined,
    result,
  };
  const state = stepState({
    status: t.live?.status ?? record?.status,
    result: t.result ? { isError: t.result.is_error } : undefined,
    verdict: t.live?.verdict ?? record?.verdict,
    approval,
    live: !!ctx.liveRun && t.runId === ctx.liveRun,
  });
  return {
    key: t.key,
    item: t,
    input,
    phrases: phrases(input),
    state,
    approval,
    record,
    link: state === 'done' || state === 'failed' ? stepLink(input) : '',
    why: state === 'blocked' ? refusalReason(result) : '',
  };
}
