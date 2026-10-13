import { describe, expect, it } from 'vitest';
import type { Message } from './api/types.gen';
import { buildItems, countToolParts, describe as describeCall, type LiveRun, type LiveTool } from './transcript';

let id = 0;
const msg = (seq: number, role: string, parts: Message['parts'], extra: Partial<Message> = {}): Message => ({
  id: ++id,
  session_id: 'ses_1',
  seq,
  role,
  kind: 'normal',
  parts,
  provenance: 'user',
  tokens: 1,
  created_at: '2026-10-12T10:00:00Z',
  ...extra,
});
const call = (cid: string, name: string) => ({ t: 'tool_call', call: { id: cid, name, args: { query: 'q' } } });
const live = (tools: Partial<LiveTool>[], extra: Partial<LiveRun> = {}): LiveRun => ({
  id: 'run_1',
  status: 'tool',
  text: '',
  thinking: '',
  skip: 0,
  tools: tools.map((t, i) => ({ callId: `tcl_${i}`, tool: 'web_search', title: '', verdict: 'auto', rule: '', out: [], ...t })),
  ...extra,
});

describe('buildItems', () => {
  it('folds tool results into their calls and hides tool messages and context parts', () => {
    const items = buildItems(
      [
        msg(1, 'user', [{ t: 'text', text: 'hi' }, { t: 'context', text: 'turn context' }]),
        msg(2, 'assistant', [{ t: 'text', text: 'looking' }, call('c1', 'web_search')], { run_id: 'run_0' }),
        msg(3, 'tool', [{ t: 'tool_result', result: { call_id: 'c1', name: 'web_search', content: 'found' } }], { run_id: 'run_0' }),
        msg(4, 'assistant', [{ t: 'text', text: 'done' }], { run_id: 'run_0' }),
      ],
      null,
    );
    expect(items.map((i) => i.kind)).toEqual(['user', 'assistant', 'tool', 'assistant']);
    const tool = items[2];
    expect(tool.kind === 'tool' && tool.result?.content).toBe('found');
    expect(items[0].kind === 'user' && items[0].text).toBe('hi');
  });

  it('renders notices, summaries and steer messages distinctly', () => {
    const items = buildItems(
      [
        msg(1, 'assistant', [{ t: 'text', text: 'summary' }], { kind: 'summary' }),
        msg(2, 'user', [{ t: 'text', text: 'also this' }], { kind: 'steer' }),
        msg(3, 'assistant', [{ t: 'text', text: 'Stopped.' }], { kind: 'notice' }),
      ],
      null,
    );
    expect(items.map((i) => i.kind)).toEqual(['summary', 'user', 'notice']);
    expect(items[1].kind === 'user' && items[1].steer).toBe(true);
  });

  it('replaces persisted tool parts of the live run with streamed rows, in order', () => {
    const messages = [msg(1, 'user', [{ t: 'text', text: 'go' }]), msg(2, 'assistant', [call('c1', 'web_search'), call('c2', 'web_fetch')], { run_id: 'run_1' })];
    const items = buildItems(messages, live([{ tool: 'web_search', status: 'ok' }, { tool: 'web_fetch' }]));
    const tools = items.filter((i) => i.kind === 'tool');
    expect(tools).toHaveLength(2);
    expect(tools.map((t) => t.kind === 'tool' && t.live?.callId)).toEqual(['tcl_0', 'tcl_1']);
  });

  it('places streamed rows whose message is not persisted yet after the run, before the stream', () => {
    const messages = [msg(1, 'user', [{ t: 'text', text: 'go' }]), msg(2, 'assistant', [{ t: 'text', text: 'step one' }], { run_id: 'run_1' })];
    const items = buildItems(messages, live([{ tool: 'shell' }], { text: 'writing…' }));
    expect(items.map((i) => i.kind)).toEqual(['user', 'assistant', 'tool', 'stream']);
  });

  it('skips tool parts persisted before attaching mid-run', () => {
    const messages = [
      msg(1, 'assistant', [call('c1', 'web_search')], { run_id: 'run_1' }),
      msg(2, 'assistant', [call('c2', 'web_search')], { run_id: 'run_1' }),
    ];
    expect(countToolParts(messages, 'run_1')).toBe(2);
    const items = buildItems(messages, live([{ tool: 'web_search' }], { skip: 1 }));
    const tools = items.filter((i) => i.kind === 'tool');
    expect(tools.map((t) => t.kind === 'tool' && !!t.live)).toEqual([false, true]);
  });

  it('keeps details of finished runs through the known map', () => {
    const known = new Map([['c1', { callId: 'tcl_9', tool: 'web_search', title: '', verdict: 'auto', rule: '', out: [], ms: 900 } as LiveTool]]);
    const items = buildItems([msg(1, 'assistant', [call('c1', 'web_search')], { run_id: 'run_0' })], null, known);
    const t = items[0];
    expect(t.kind === 'tool' && t.live?.ms).toBe(900);
  });
});

describe('describe', () => {
  it('summarises calls from the Action first', () => {
    expect(describeCall('shell', { tool: 'shell', kind: 'exec', side_effect: 'local_write', argv: ['bash', '-lc', 'python analyze.py'], payload: {}, fingerprint: '' })).toBe('Ran `python analyze.py`');
    expect(describeCall('web_fetch', { tool: 'web_fetch', kind: 'http', side_effect: 'read', host: 'example.com', url: 'https://example.com/a', payload: {}, fingerprint: '' })).toBe('Fetched example.com/a');
    expect(describeCall('x', { tool: 'x', kind: 'mcp', side_effect: 'read', mcp_server: 'docs', mcp_tool: 'search', payload: {}, fingerprint: '' })).toBe('Used docs › search');
  });

  it('falls back to tool name and arguments', () => {
    expect(describeCall('web_search', undefined, { query: 'madrid weather' })).toBe('Searched the web: “madrid weather”');
    expect(describeCall('mcp__notes__read_file')).toBe('Used notes › read_file');
    expect(describeCall('custom_tool', undefined, {}, 'Custom title')).toBe('Custom title');
  });
});

describe('buildItems keys', () => {
  // Svelte throws on duplicate keys in a keyed each, which would take the
  // whole conversation down; providers do repeat call ids.
  it('stays unique when a message repeats a tool call id', () => {
    const items = buildItems([msg(1, 'assistant', [call('dup', 'web_search'), call('dup', 'web_search')], { run_id: 'run_0' })], null);
    const keys = items.map((i) => i.key);
    expect(new Set(keys).size).toBe(keys.length);
    expect(items.filter((i) => i.kind === 'tool')).toHaveLength(2);
  });
});
