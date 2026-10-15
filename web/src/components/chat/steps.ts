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

// The deploy refusals that already read as plain sentences; the person can act on them.
const plainRelease = [/^a deploy to .+ needs .+ other than you/, /^the .+ environment has no approvers$/, /^deploys can only be requested from your own chats/, /^could not check who may approve this deploy$/];

/**
 * Why "Denied by policy (<rule>): <reason>" refused a step, as a sentence. Other
 * refusals name rules, tools and errors, so they get one plain sentence and the
 * server's words stay in the step's Details.
 */
export function refusalReason(result: string | undefined): string {
  const m = /^Denied by policy \(([^)]*)\): (.+)$/s.exec(result?.trim() ?? '');
  if (!m) return '';
  const rule = m[1];
  const reason = m[2].trim();
  if (rule === 'floor:paused') return 'Bothy is paused.';
  if (rule === 'floor:release') {
    if (plainRelease.some((re) => re.test(reason))) return reason.charAt(0).toUpperCase() + reason.slice(1) + (/[.!?]$/.test(reason) ? '' : '.');
    if (/^deploys are not allowed in /.test(reason)) return 'Deploys can only be asked for by a person in a chat.';
    const env = /^this deploy does not match the (.+) environment: /.exec(reason);
    if (env) return `This deploy doesn't match how ${env[1]} is set up.`;
  }
  return "Bothy's rules don't allow this.";
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
