import { fireEvent, render, screen } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import type { ToolCallRecord } from '../../lib/api/types.gen';
import { stepLine } from './steps';
import StepItem from './StepItem.svelte';

const call = (tool: string): ToolCallRecord =>
  ({ id: `tc_${tool}`, run_id: 'run_1', tool, status: 'ok', verdict: 'allow', rule: '', untrusted: true, duration: 1e9, action: { tool, kind: 'generic', side_effect: 'read', payload: {} } }) as never;

describe('StepItem details', () => {
  // Only web_fetch reads a web page; the line used to say so for every outside source.
  it.each([
    ['knowledge_search', 'a repository'],
    ['mcp__acme-helpdesk__echo', 'a connected tool'],
    ['web_fetch', 'a web page'],
  ])('names where %s outside content came from', async (tool, source) => {
    render(StepItem, { line: stepLine(call(tool)), how: '' });
    await fireEvent.click(screen.getByRole('button'));
    expect(screen.getByText(`This came from outside your team (${source}). Bothy treats it with caution.`)).toBeTruthy();
  });
});
