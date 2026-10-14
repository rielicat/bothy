import { fireEvent, render, screen, waitFor, within } from '@testing-library/svelte';
import { beforeEach, describe, expect, it } from 'vitest';
import { resetMock } from '../../../../mock/server';
import type { DoctorReport, Finding } from '../../../lib/api/types.gen';
import { config } from '../../../lib/config.svelte';
import { session } from '../../../lib/stores/session.svelte';
import { installApi, type Call } from '../../../test/api';
import Advanced from './Advanced.svelte';
import { matchesOf, readRule, verdictSentence, whenParts } from './rules';

async function open(opts: { mock?: string; override?: (m: string, url: string) => unknown } = {}): Promise<Call[]> {
  const calls = installApi({ override: opts.override });
  if (opts.mock) resetMock({ mock: opts.mock });
  session.ready = false;
  await session.init();
  config.view = null;
  render(Advanced);
  await screen.findByText('Settings for troubleshooting and fine-tuning. Most teams never need these.');
  return calls;
}

describe('Advanced', () => {
  beforeEach(() => {
    config.view = null;
  });

  it('has every section of §11.7 in order', async () => {
    await open();
    const titles = screen.getAllByRole('heading', { level: 2 }).map((h) => h.textContent);
    expect(titles).toEqual(['Health check', 'Configuration file', 'Backup', 'Rules', 'Limits', 'Running code', 'Network', 'Model prices', 'Built-in safety rules', 'Logs']);
  });

  it('runs the health check on open and says when nothing is wrong', async () => {
    await open({ mock: 'no-findings' });
    expect(await screen.findByText('No problems found.')).toBeTruthy();
    expect(document.body.textContent).toMatch(/No problems found\. Last checked just now\./);
  });

  // Keyed {#each} blocks throw on a repeated key; one check can report several findings (moved from keys.test.ts).
  it('lists every doctor finding when one check reports several', async () => {
    const f: Finding = { id: 'mcp.drifted', area: 'mcp', title: 'Server changed', detail: 'docs', severity: 'medium', security: true, auto_fix: true };
    const report: DoctorReport = { findings: [f, { ...f, detail: 'search' }] };
    await open({ override: (_m, url) => (url === '/api/doctor' ? report : undefined) });
    await fireEvent.click(await screen.findByRole('button', { name: 'Run again' }));
    await waitFor(() => expect(screen.getAllByText('Server changed').length).toBeGreaterThanOrEqual(2));
  });

  it('shows the effective configuration with where each value comes from', async () => {
    await open();
    const block = screen.getByRole('group', { name: 'Effective configuration' });
    expect(block.textContent).toContain('# file · bothy.yaml:5');
    expect(screen.getByRole('link', { name: 'Download bothy.ui.yaml' }).getAttribute('download')).toBe('bothy.ui.yaml');
  });

  it('backs up and says where the file went', async () => {
    await open();
    await fireEvent.click(screen.getByRole('button', { name: 'Back up now' }));
    expect(await screen.findByText(/^Saved to \/data\/backups\/bothy-.+\.tar\.$/)).toBeTruthy();
    expect(document.body.textContent).toMatch(/Last backup: just now/);
  });

  it('edits a rule and writes it back whole', async () => {
    const calls = await open();
    await fireEvent.click(screen.getByRole('button', { name: 'Edit rule wiki' }));
    const dialog = screen.getByRole('dialog', { name: 'Edit rule wiki' });
    expect((within(dialog).getByLabelText(/Websites/) as HTMLInputElement).value).toBe('*.wikipedia.org');
    await fireEvent.input(within(dialog).getByLabelText(/Reason shown/), { target: { value: 'Reading the wiki is fine' } });
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Save rule' }));
    await waitFor(() => expect(calls.some((c) => c.method === 'PATCH')).toBe(true));
    expect(JSON.parse(calls.find((c) => c.method === 'PATCH')!.body).set['policy.rules.wiki']).toMatchObject({
      id: 'wiki',
      tool: 'web_fetch',
      domains: ['*.wikipedia.org'],
      verdict: 'auto',
      reason: 'Reading the wiki is fine',
    });
  });

  it('checks a new rule before saving it', async () => {
    const calls = await open();
    await fireEvent.click(screen.getByRole('button', { name: 'Add rule' }));
    const dialog = screen.getByRole('dialog', { name: 'Add rule' });
    await fireEvent.input(within(dialog).getByLabelText('Name'), { target: { value: 'Wiki' } });
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Save rule' }));
    expect(within(dialog).getByText('Use lowercase letters, numbers and dashes.')).toBeTruthy();
    expect(within(dialog).getByText('Name the action, for example shell or web_fetch.')).toBeTruthy();
    expect(calls.some((c) => c.method === 'PATCH')).toBe(false);
  });

  it('tests a rule and answers in a sentence', async () => {
    await open();
    await fireEvent.click(screen.getByRole('button', { name: 'Test a rule…' }));
    const dialog = screen.getByRole('dialog', { name: 'Test a rule' });
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Check' }));
    expect(await within(dialog).findByText('Bothy would ask, because of the approval setting for actions that change files.')).toBeTruthy();
    await fireEvent.input(within(dialog).getByLabelText('Details (JSON)'), { target: { value: '{nope' } });
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Check' }));
    expect(within(dialog).getByText('Write the details as a JSON object, like {"command": "ls"}.')).toBeTruthy();
  });

  it('marks changed and environment values, and nowhere else', async () => {
    await open();
    expect(screen.getAllByText(/Changed ·/).length).toBeGreaterThanOrEqual(1);
    expect(screen.getByText('Set by the server environment (BOTHY_LOG__LEVEL). Change it there.')).toBeTruthy();
    expect((screen.getByLabelText('Log level') as HTMLSelectElement).disabled).toBe(true);
  });

  it('edits a model price', async () => {
    const calls = await open();
    await fireEvent.click(await screen.findByRole('button', { name: /^Edit prices for/ }));
    const dialog = screen.getByRole('dialog');
    await fireEvent.input(within(dialog).getByLabelText('Input per million'), { target: { value: '0.5' } });
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Save prices' }));
    await waitFor(() => expect(calls.some((c) => c.method === 'PATCH')).toBe(true));
    expect(JSON.parse(calls.find((c) => c.method === 'PATCH')!.body).set).toEqual({ 'pricing.ollama/qwen3:14b': { in: 0.5, out: 0 } });
  });

  it('shows the built-in safety rules read-only', async () => {
    await open();
    expect(await screen.findByText(/Never reveal saved keys/)).toBeTruthy();
  });
});

describe('rules', () => {
  const values: Record<string, unknown> = {
    'policy.rules.x.tool': 'shell',
    'policy.rules.x.argv0': ['ls', 'cat'],
    'policy.rules.x.verdict': 'deny',
    'policy.rules.x.match./argv/2': '*curl*',
  };

  it('reads a rule from its config entries', () => {
    const r = readRule('x', (f) => values[`policy.rules.x.${f}`]);
    expect(r).toMatchObject({ id: 'x', tool: 'shell', argv0: ['ls', 'cat'], verdict: 'deny', domains: [] });
    const match = matchesOf('x', Object.keys(values), (p) => values[p]);
    expect(match).toEqual({ '/argv/2': '*curl*' });
    expect(whenParts({ ...r, match })).toEqual(['commands ls, cat', '/argv/2 matches *curl*']);
    expect(readRule('y', () => undefined).verdict).toBe('ask');
  });

  it('says why Bothy would decide what it decides', () => {
    expect(verdictSentence({ verdict: 'ask', rule: 'rule:wiki-writes', reason: '' })).toBe('Bothy would ask, because rule wiki-writes matches.');
    expect(verdictSentence({ verdict: 'auto', rule: 'grant:grt_1', reason: '' })).toBe('Bothy would do it without asking, because a permission allows it.');
    expect(verdictSentence({ verdict: 'deny', rule: 'floor:financial', reason: '' })).toBe('Bothy would refuse, because a built-in safety rule applies.');
    expect(verdictSentence({ verdict: 'ask', rule: 'first_contact:domain', reason: '' })).toBe("Bothy would ask, because it's the first time Bothy contacts this website.");
    expect(verdictSentence({ verdict: 'ask', rule: 'default:external_send', reason: '' })).toBe('Bothy would ask, because of the approval setting for actions that send information out.');
    expect(verdictSentence({ verdict: 'ask', rule: 'odd', reason: 'Writes files.' })).toBe('Bothy would ask: Writes files.');
    expect(verdictSentence({ verdict: 'auto', rule: 'odd', reason: '' })).toBe('Bothy would do it without asking.');
  });
});
