import { describe, expect, it } from 'vitest';
import { installApi } from '../../test/api';
import { setCsrf } from './client';
import {
  failReason,
  fullness,
  githubState,
  githubUrl,
  knowledge,
  linesLabel,
  marks,
  plainText,
  progressPct,
  projectHref,
  queryWords,
  readRepos,
  repoState,
} from './knowledge';

describe('repository state', () => {
  it('maps index status and staleness to the four list states', () => {
    expect(repoState({ status: 'ready' })).toBe('ready');
    expect(repoState({ status: 'ready', stale: true })).toBe('stale');
    expect(repoState({ status: 'queued' })).toBe('reading');
    expect(repoState({ status: 'indexing' })).toBe('reading');
    expect(repoState({ status: 'error', stale: true })).toBe('failed');
  });

  it('reports whole percents and never 100 while reading', () => {
    expect(progressPct({ progress: { done: 480, total: 1200 } })).toBe(40);
    expect(progressPct({ progress: { done: 1200, total: 1200 } })).toBe(99);
    expect(progressPct({})).toBeNull();
    expect(progressPct({ progress: { done: 3, total: 0 } })).toBeNull();
  });

  it('says why a read failed in plain words', () => {
    expect(failReason({ error: 'walk stopped: more than 50000 files (knowledge.max_files)', truncated: true })).toBe('Too large to read (more than 50,000 files).');
    expect(failReason({ error: 'github: 404 Not Found', truncated: false })).toBe('GitHub says Bothy no longer has access to this repository.');
    expect(failReason({ error: 'dial tcp: i/o timeout', truncated: false })).toBe("Bothy couldn't reach GitHub. It will try again later.");
    expect(failReason({ error: 'panic: nil map', truncated: false })).toBe('An unexpected error stopped the read.');
    // The cap is configurable: never a number the message didn't give, and a rate limit is not a size problem.
    expect(failReason({ error: 'walk stopped: more than 20000 files', truncated: true })).toBe('Too large to read (more than 20,000 files).');
    expect(failReason({ error: 'walk stopped', truncated: true })).toBe('Too large to read.');
    expect(failReason({ error: 'github: API rate limit exceeded', truncated: false })).toBe('GitHub is limiting how often Bothy can read. Try again later.');
  });
});

describe('notes text', () => {
  it('turns a Markdown snippet into one plain line', () => {
    expect(plainText('- Login redirects stay on `our` domain. - **We** stay [here](x).')).toBe('Login redirects stay on our domain. We stay here.');
  });

  it('is near full above 80 % of the cap and over past it, counting UTF-8 bytes', () => {
    expect(fullness('a'.repeat(80), 100)).toBe('ok');
    expect(fullness('a'.repeat(81), 100)).toBe('near');
    expect(fullness('é'.repeat(51), 100)).toBe('over');
    expect(fullness('anything', 0)).toBe('ok');
  });
});

describe('search helpers', () => {
  it('highlights every query word, case-insensitively', () => {
    const words = queryWords('Login  redirect a');
    expect(words).toEqual(['login', 'redirect']);
    expect(marks('loginRedirect(url)', words)).toEqual([
      { text: 'login', hit: true },
      { text: 'Redirect', hit: true },
      { text: '(url)', hit: false },
    ]);
    expect(marks('a+b (c)', queryWords('(c)'))).toEqual([{ text: 'a+b (c)', hit: false }]);
  });

  it('names line ranges in words and links GitHub at the commit', () => {
    expect(linesLabel(10, 42)).toBe('lines 10–42');
    expect(linesLabel(7, 7)).toBe('line 7');
    expect(githubUrl('acme/web')).toBe('https://github.com/acme/web');
    expect(githubUrl('acme/web', { sha: '4f2c1a9', path: 'src/a b.ts', start: 10, end: 12 })).toBe('https://github.com/acme/web/blob/4f2c1a9/src/a%20b.ts#L10-L12');
    expect(projectHref('acme/web')).toBe('/knowledge/projects/acme/web');
  });
});

describe('knowledge API', () => {
  it('saves notes with the quoted etag in If-Match', async () => {
    const calls = installApi();
    setCsrf('mock-csrf');
    const n = await knowledge.notes(1);
    await knowledge.saveNotes(1, `${n.content}\nMore.`, n.etag);
    const put = calls.find((c) => c.method === 'PUT');
    expect(put?.url).toBe('/api/knowledge/repos/1/notes');
    expect(put?.headers['if-match']).toBe(`"${n.etag}"`);
  });

  it('treats a server without knowledge as "not yet", but a history that fails as an error', async () => {
    installApi({ override: (_m, url) => (url.includes('/notes/history') || url === '/api/knowledge/repos' ? new Response('{}', { status: 404 }) : undefined) });
    await expect(knowledge.history(1)).rejects.toThrow();
    expect(await readRepos()).toEqual([]);
  });

  it('reads the notes history with names and chats', async () => {
    installApi();
    const list = await knowledge.history(1);
    expect(list.map((c) => c.id)).toEqual([3, 2, 1]);
    expect(list[0]).toMatchObject({ actor: 'user', name: 'Priya Rao' });
    expect(list[1].chat).toEqual({ id: 'ses_01jlogin00000000000000000', title: 'Login bug' });
  });

  it('asks members only for the repositories, not the connections', async () => {
    const calls = installApi();
    expect(await githubState(false)).toEqual({ connected: null, allowed: ['acme/web', 'acme/api'] });
    expect(calls.some((c) => c.url === '/api/github/connections')).toBe(false);
    expect((await githubState(true)).connected).toBe(true);
  });
});
