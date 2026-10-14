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
  return { key: t.key, item: t, input, phrases: phrases(input), state, approval, record, link: state === 'done' || state === 'failed' ? stepLink(input) : '' };
}
