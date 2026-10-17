import { describe, expect, it } from 'vitest';
import { diffCounts, fill, lineFor, phrases, plain, shortCommand, stepLink, stepState, summarize, type StepInput, type StepState } from './activity';
import type { Action } from './api/types.gen';

const act = (a: Partial<Action>): Action => ({ tool: 'x', kind: 'generic', side_effect: 'read', payload: {}, fingerprint: '', ...a });

// Every tool the §7.4 table names, with arguments that fill its placeholders.
const tools: [string, StepInput][] = [
  ['fs_read', { tool: 'fs_read', args: { path: 'package.json' } }],
  ['fs_list', { tool: 'fs_list', args: { path: 'notes' } }],
  ['fs_write create', { tool: 'fs_write', args: { path: 'a.md', content: 'x', mode: 'create' } }],
  ['fs_write overwrite', { tool: 'fs_write', args: { path: 'a.md', content: 'x' } }],
  ['fs_write append', { tool: 'fs_write', args: { path: 'a.md', content: 'x', mode: 'append' } }],
  ['fs_delete', { tool: 'fs_delete', args: { path: 'notes/staging.md' } }],
  ['shell', { tool: 'shell', args: { command: 'npm test' } }],
  ['python', { tool: 'python', args: { code: 'print(1)' } }],
  ['web_search', { tool: 'web_search', args: { query: 'q3 pricing' } }],
  ['web_fetch GET', { tool: 'web_fetch', args: { url: 'https://api.github.com/repos' } }],
  ['web_fetch other', { tool: 'web_fetch', args: { url: 'https://hooks.example/x', method: 'POST' } }],
  ['net_allow', { tool: 'net_allow', action: act({ tool: 'net_allow', kind: 'net', host: 'pypi.org' }) }],
  ['memory', { tool: 'memory', args: { file: 'MEMORY', op: 'add', new: 'Deploys happen on Tuesdays' } }],
  ['memory replace', { tool: 'memory', args: { file: 'MEMORY', op: 'replace', old: 'Mondays', new: 'Deploys happen on Tuesdays' } }],
  ['memory remove', { tool: 'memory', args: { file: 'MEMORY', op: 'remove', old: 'Deploys happen on Tuesdays' } }],
  ['memory for everyone', { tool: 'memory', args: { file: 'TEAM', op: 'add', new: 'Deploy day is Thursday' } }],
  ['memory for everyone, replace', { tool: 'memory', args: { file: 'TEAM', op: 'replace', old: 'Tuesday', new: 'Thursday' } }],
  ['memory for everyone, remove', { tool: 'memory', args: { file: 'TEAM', op: 'remove', old: 'Deploy day is Thursday' } }],
  ['session_search', { tool: 'session_search', args: { query: 'launch' } }],
  ['message', { tool: 'message', action: act({ tool: 'message', kind: 'send', recipient: '#eng' }) }],
  ['skill_load', { tool: 'skill_load', args: { name: 'Release notes' } }],
  ['skill_read', { tool: 'skill_read', args: { name: 'Release notes' } }],
  ['schedule_list', { tool: 'schedule_list', args: {} }],
  ['schedule_create', { tool: 'schedule_create', args: { name: 'Monday summary' } }],
  ['schedule_delete', { tool: 'schedule_delete', args: { name: 'Monday summary' } }],
  ['repo_list', { tool: 'repo_list', args: {} }],
  ['repo_checkout', { tool: 'repo_checkout', args: { repo: 'acme/web', ref: 'main' } }],
  ['repo_changes', { tool: 'repo_changes', args: { repo: 'acme/web' } }],
  ['repo_open_pr', { tool: 'repo_open_pr', args: { repo: 'acme/web' } }],
  ['github_issues', { tool: 'github_issues', args: { repo: 'acme/web' } }],
  ['github_pr', { tool: 'github_pr', args: { repo: 'acme/web', number: 482 } }],
  ['github_pr list', { tool: 'github_pr', args: { repo: 'acme/web', state: 'open' } }],
  ['github_comment', { tool: 'github_comment', args: { repo: 'acme/web', number: 482 } }],
  ['github_runs', { tool: 'github_runs', args: { repo: 'acme/web' } }],
  ['release_deploy', { tool: 'release_deploy', args: { repo: 'acme/web', environment: 'production' } }],
  ['release_status', { tool: 'release_status', args: { environment: 'staging' } }],
  ['knowledge_search', { tool: 'knowledge_search', args: { query: 'redirect' } }],
  ['notes', { tool: 'notes', args: { repo: 'acme/web' } }],
  ['mcp', { tool: 'mcp__linear__create_issue', args: {} }],
  ['unknown', { tool: 'make_coffee', args: {} }],
];

describe('phrases', () => {
  it.each(tools)('%s has every tense, filled', (_, input) => {
    const p = phrases(input);
    for (const tense of ['ask', 'ing', 'doing', 'done', 'failed'] as const) {
      const t = plain(p[tense]);
      expect(t, tense).not.toBe('');
      expect(t, tense).not.toMatch(/[{}[\]]/);
    }
  });

  it('says a pending delete is waiting, and past tense only once it is done', () => {
    const p = phrases({ tool: 'fs_delete', args: { path: 'notes/staging.md' } });
    expect(plain(lineFor(p, 'waiting'))).toBe('Waiting for approval to delete notes/staging.md');
    expect(plain(lineFor(p, 'running'))).toBe('Deleting notes/staging.md…');
    expect(plain(lineFor(p, 'done'))).toBe('Deleted notes/staging.md');
    expect(plain(lineFor(p, 'failed'))).toBe("Couldn't delete notes/staging.md");
    for (const s of ['denied', 'expired', 'blocked'] as StepState[]) expect(plain(lineFor(p, s))).toBe("Didn't delete notes/staging.md");
    expect(plain(p.ing)).toBe('deleting notes/staging.md');
  });

  it('keeps paths, hosts and commands verbatim in code segments, and names in the body font', () => {
    expect(phrases({ tool: 'shell', args: { command: 'npm test' } }).done).toEqual([
      { k: 'text', v: 'Ran ' },
      { k: 'code', v: 'npm test' },
    ]);
    expect(phrases({ tool: 'web_search', args: { query: 'q3 pricing' } }).done).toEqual([
      { k: 'text', v: 'Searched the web for “' },
      { k: 'name', v: 'q3 pricing' },
      { k: 'text', v: '”' },
    ]);
    expect(plain(phrases({ tool: 'web_fetch', args: { url: 'https://api.github.com/repos/acme' } }).done)).toBe('Read api.github.com/repos/acme');
    expect(plain(phrases({ tool: 'fs_list', args: {} }).done)).toBe("Looked in Bothy's files");
  });

  it('prefers the system-built action, and counts a change from its diff', () => {
    const a = act({ tool: 'fs_write', kind: 'fs.write', paths: ['src/auth.ts'], diff: '--- a/src/auth.ts\n+++ b/src/auth.ts\n@@ -1 +1,2 @@\n-old\n+new\n+more' });
    expect(plain(phrases({ tool: 'fs_write', action: a, args: { path: 'ignored' } }).done)).toBe('Changed src/auth.ts (+2\u00a0−1)');
    expect(plain(phrases({ tool: 'fs_write', action: act({ ...a, diff: '--- /dev/null\n+++ b/n.md\n+x' }) }).done)).toBe('Created src/auth.ts');
    expect(plain(phrases({ tool: 'shell', action: act({ argv: ['bash', '-lc', 'ls -la'] }) }).done)).toBe('Ran ls -la');
    expect(diffCounts('+++ b\n--- a\n+x\n-y\n-z')).toEqual({ a: 1, r: 2 });
  });

  it('names the chat a message goes to by its app, never by its key', () => {
    const msg = (recipient: string) => phrases({ tool: 'message', action: act({ tool: 'message', kind: 'send', recipient }) });
    expect(plain(msg('telegram:42:').done)).toBe('Sent a message to the Telegram chat');
    expect(plain(lineFor(msg('slack:C04ENG:17.2'), 'waiting'))).toBe('Waiting for approval to send a message to the Slack chat');
    expect(plain(msg('').done)).toBe('Sent a message to this chat');
  });

  it('names MCP tools by server and title', () => {
    expect(plain(phrases({ tool: 'mcp__linear__create_issue' }).done)).toBe('Used Linear: Create issue');
    expect(plain(phrases({ tool: 'mcp__linear__create_issue', title: 'File an issue' }).ask)).toBe('use Linear: File an issue');
    expect(plain(phrases({ tool: 'make_coffee' }).failed)).toBe('make_coffee failed');
  });

  it('quotes the remembered entry, and never shows empty quotes', () => {
    expect(plain(phrases({ tool: 'memory', args: { file: 'MEMORY', op: 'add', new: 'Staging is staging.acme.test' } }).done)).toBe('Remembered “Staging is staging.acme.test”');
    expect(plain(phrases({ tool: 'memory', args: { file: 'MEMORY', op: 'remove', old: 'Old fact' } }).done)).toBe('Forgot “Old fact”');
    expect(plain(phrases({ tool: 'memory', args: { file: 'MEMORY', op: 'replace', old: 'a', new: 'b' } }).done)).toBe('Updated a saved note to “b”');
    expect(plain(phrases({ tool: 'memory', args: { file: 'MEMORY', op: 'add' } }).done)).toBe('Remembered something');
  });

  it('says a note for everyone is for everyone, and waits for approval', () => {
    const team = (args: Record<string, unknown>) => phrases({ tool: 'memory', args: { file: 'TEAM', ...args } });
    expect(plain(team({ op: 'add', new: 'Deploy day is Thursday' }).done)).toBe('Remembered “Deploy day is Thursday” for everyone');
    expect(plain(lineFor(team({ op: 'add', new: 'Deploy day is Thursday' }), 'waiting'))).toBe('Waiting for approval to remember something for everyone');
    expect(plain(team({ op: 'replace', old: 'a', new: 'b' }).done)).toBe('Updated a note for everyone');
    expect(plain(team({ op: 'remove', old: 'Old fact' }).done)).toBe('Forgot “Old fact” for everyone');
  });

  it("doesn't repeat the server's internal title on an MCP step", () => {
    const p = phrases({ tool: 'mcp__deepwiki__read_wiki_structure', title: 'MCP deepwiki: read_wiki_structure', args: {} });
    expect(plain(p.ask)).not.toContain('MCP');
    expect(plain(p.ask)).toContain('Read wiki structure');
  });

  it('says listing pull requests, not "pull request #", when no number is given', () => {
    expect(plain(phrases({ tool: 'github_pr', args: { repo: 'acme/web', state: 'open' } }).done)).toBe('Listed pull requests in acme/web');
    expect(plain(phrases({ tool: 'github_pr', args: { repo: 'acme/web', number: 7 } }).done)).toBe('Read pull request #7 in acme/web');
  });

  it('drops an optional part when its value is missing', () => {
    expect(plain(phrases({ tool: 'repo_checkout', args: { repo: 'acme/web' } }).done)).toBe('Got a copy of acme/web');
    expect(plain(phrases({ tool: 'repo_open_pr', args: { repo: 'acme/web' } }).done)).toBe('Opened pull request in acme/web');
    expect(plain(phrases({ tool: 'repo_changes', args: { repo: 'acme/web' }, result: 'repos/acme/web, checked out at main (4f2c1a9d2e8b) on Oct 14.\nNo changes since the checkout.\n' }).done)).toBe(
      'Reviewed changes in acme/web',
    );
    expect(fill('a[ {x}] b', {})).toEqual([{ k: 'text', v: 'a b' }]);
  });
});

// The results as the GitHub and release tools write them (internal/github, internal/release).
describe('results', () => {
  const pr = 'Opened pull request #482 as @octocat: https://github.com/acme/web/pull/482\nBranch bothy/fix-login → main, one commit, 1 file.';

  it('reads the pull request number and link', () => {
    const step = { tool: 'repo_open_pr', args: { repo: 'acme/web' }, result: pr };
    expect(plain(phrases(step).done)).toBe('Opened pull request #482 in acme/web');
    expect(stepLink(step)).toBe('https://github.com/acme/web/pull/482');
    expect(stepLink({ tool: 'repo_open_pr', result: 'Opened pull request #7 as @bothy[bot]: https://github.com/acme/web/pull/7' })).toBe('https://github.com/acme/web/pull/7');
    expect(stepLink({ tool: 'repo_open_pr', result: 'Opened pull request #482: https://github.com/acme/web/pull/482' })).toBe('https://github.com/acme/web/pull/482');
    expect(stepLink({ tool: 'repo_open_pr', result: 'Opened pull request #482: javascript:alert(1)' })).toBe('');
  });

  it('links a comment and a started deploy to GitHub', () => {
    expect(stepLink({ tool: 'github_comment', result: 'Commented on acme/web#482: https://github.com/acme/web/pull/482#issuecomment-1' })).toBe(
      'https://github.com/acme/web/pull/482#issuecomment-1',
    );
    const deploy =
      'Started deploy.yml on acme/web at main for production. Run 1003: https://github.com/acme/web/actions/runs/1003. Approved by Priya Shah and Sam Lee. The outcome will be posted here when the run finishes.';
    expect(stepLink({ tool: 'release_deploy', result: deploy })).toBe('https://github.com/acme/web/actions/runs/1003');
    expect(stepLink({ tool: 'release_deploy', result: 'Started deploy.yml on acme/web at main for production. The outcome will be posted here when the run finishes.' })).toBe('');
    // An approver's name is never taken for the run's link.
    const spoofed = 'Started deploy.yml on acme/web at main for production. Approved by Priya and Eve. Run 1: https://evil.example/x. The outcome will be posted here when the run finishes.';
    expect(stepLink({ tool: 'release_deploy', result: spoofed })).toBe('');
  });

  it('says a deploy started, not that it is done', () => {
    const p = phrases({ tool: 'release_deploy', args: { repo: 'acme/web', environment: 'production' } });
    expect(plain(p.done)).toBe('Started deploying acme/web to production');
  });

  it('counts the files repo_changes found', () => {
    const changes = 'repos/acme/web, checked out at main (4f2c1a9d2e8b) on Oct 14.\n3 changed files not in a pull request yet:\nM src/a.ts\nA src/b.ts\nD old.md\n';
    expect(plain(phrases({ tool: 'repo_changes', args: { repo: 'acme/web' }, result: changes }).done)).toBe('Reviewed changes in acme/web (3 files)');
    const older = 'repos/acme/web, checked out at main (4f2c1a9d2e8b) on Oct 14.\n2 changed files:\nM src/a.ts\nA src/b.ts\n';
    expect(plain(phrases({ tool: 'repo_changes', args: { repo: 'acme/web' }, result: older }).done)).toBe('Reviewed changes in acme/web (2 files)');
  });

  it("never links what another tool's result says", () => {
    expect(stepLink({ tool: 'web_fetch', result: 'Opened pull request #1: https://evil.example' })).toBe('');
    expect(stepLink({ tool: 'mcp__github__create_issue', result: '{"html_url":"https://evil.example"}' })).toBe('');
  });
});

describe('stepState', () => {
  it('waits while an approval is open, whatever else has arrived', () => {
    expect(stepState({ live: true, approval: { status: 'pending' }, status: 'waiting', verdict: 'ask' })).toBe('waiting');
    expect(stepState({ live: false, approval: { status: 'parked' }, result: { isError: false } })).toBe('waiting');
  });

  it('is never done before the result', () => {
    expect(stepState({ live: true, approval: { status: 'allowed' }, verdict: 'ask' })).toBe('running');
    expect(stepState({ live: true, verdict: 'auto' })).toBe('running');
    expect(stepState({ live: true, verdict: 'ask' })).toBe('waiting');
    expect(stepState({ live: true, approval: { status: 'allowed' }, status: 'ok' })).toBe('done');
    expect(stepState({ live: false, result: { isError: false } })).toBe('done');
  });

  it('tells failures from refusals', () => {
    expect(stepState({ live: false, result: { isError: true } })).toBe('failed');
    expect(stepState({ live: true, status: 'timeout' })).toBe('failed');
    expect(stepState({ live: false, approval: { status: 'denied' }, result: { isError: true } })).toBe('denied');
    expect(stepState({ live: false, approval: { status: 'expired' } })).toBe('expired');
    expect(stepState({ live: true, verdict: 'deny' })).toBe('blocked');
    expect(stepState({ live: false, status: 'denied' })).toBe('blocked');
  });
});

describe('summarize', () => {
  const step = (input: StepInput) => ({ input, phrases: phrases(input) });
  const read = (path: string) => step({ tool: 'fs_read', args: { path } });

  it('names one or two kinds of step', () => {
    expect(plain(summarize([read('src/a.ts'), read('src/b.ts'), read('src/c.ts')]))).toBe('Read 3 files in src');
    expect(plain(summarize([read('a.ts'), read('docs/b.md'), step({ tool: 'web_search', args: { query: 'x' } })]))).toBe('Read 2 files and searched the web');
    expect(plain(summarize([step({ tool: 'repo_checkout', args: { repo: 'acme/web', ref: 'main' } }), step({ tool: 'shell', args: { command: 'npm test' } })]))).toBe(
      'Got a copy of acme/web and ran npm test',
    );
  });

  it('counts the steps when there are more kinds', () => {
    const many = [read('a'), step({ tool: 'web_search', args: {} }), step({ tool: 'shell', args: { command: 'ls' } })];
    expect(plain(summarize(many))).toBe('Took 3 steps');
  });

  it("names folders inside a repository the summary already names by the repository's own path", () => {
    const files = ['session.ts', 'callback.ts', 'redirect.ts'].map((f) => read(`repos/acme/web/src/auth/${f}`));
    expect(plain(summarize(files))).toBe('Read 3 files in acme/web · src/auth');
    expect(plain(summarize([step({ tool: 'repo_checkout', args: { repo: 'acme/web' } }), ...files]))).toBe('Got a copy of acme/web and read 3 files in src/auth');
  });
});

describe('paths inside a repository', () => {
  it('read like citations: the repository, then the path inside it in code', () => {
    const p = phrases({ tool: 'fs_write', args: { path: 'repos/acme/web/src/auth/callback.ts', mode: 'overwrite' } });
    expect(p.done.slice(1, 3)).toEqual([
      { k: 'repo', v: 'acme/web' },
      { k: 'code', v: 'src/auth/callback.ts' },
    ]);
    expect(plain(p.done)).toBe('Changed acme/web · src/auth/callback.ts');
    expect(plain(phrases({ tool: 'fs_list', args: { path: 'repos/acme/web' } }).done)).toBe('Looked in acme/web');
    // Elsewhere in Bothy's files a path stays as it is.
    expect(plain(phrases({ tool: 'fs_read', args: { path: 'notes/repos/acme/web/a.md' } }).done)).toBe('Read notes/repos/acme/web/a.md');
  });
});

describe('waiting line', () => {
  it("says only whose approval it waits for when the card follows right below", () => {
    const p = phrases({ tool: 'fs_delete', args: { path: 'notes/staging.md' } });
    expect(plain(lineFor(p, 'waiting', 'yours'))).toBe('Waiting for your approval');
    expect(plain(lineFor(p, 'waiting', 'theirs'))).toBe('Waiting for approval');
    expect(plain(lineFor(p, 'waiting'))).toBe('Waiting for approval to delete notes/staging.md');
  });
});

describe('shortCommand', () => {
  it('keeps short commands and drops a leading cd', () => {
    expect(shortCommand('npm test')).toBe('npm test');
    expect(shortCommand('cd repos/acme/api && go test ./...')).toBe('go test ./...');
  });
  it('cuts a long command at its first separator outside quotes', () => {
    expect(shortCommand('cd repos/acme/api && find . -type f | head; echo ---; cat * 2>/dev/null | head -50')).toBe('find . -type f …');
    expect(shortCommand(`uv run --with humanize python -c "import humanize; print(humanize.intcomma(1234567))"`)).toBe(
      'uv run --with humanize python -c "import humanize; print(hu…',
    );
    expect(shortCommand('go build ./... 2>&1 && go vet ./... 2>&1 && go test -race -count=1 ./internal/...')).toBe('go build ./... 2>&1 …');
  });
});
