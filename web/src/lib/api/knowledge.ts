// Knowledge API (docs/design/ui.md §9): repositories with their reading
// progress, notes and the notes' history, and search over code.
import { api, ApiError, qs } from './client';
import { listRepos } from './repos';
import type { IndexedRepo, KnowledgeHit as Hit, KnowledgeNotes, List, NotesChange } from './types.gen';

export type { KnowledgeNotes, NotesChange };
export { isUnavailable, readRepos } from './repos';

export type KnowledgeRepo = IndexedRepo;

export type KnowledgeHit = Hit & {
  /** S18: where the hit came from; the server searches code only today. */
  source?: 'code' | 'notes';
};

const repoPath = (id: number) => `/api/knowledge/repos/${id}`;

export const knowledge = {
  repos: listRepos,
  notes: (id: number) => api.get<KnowledgeNotes>(`${repoPath(id)}/notes`),
  saveNotes: (id: number, content: string, etag: string) => api.put<KnowledgeNotes>(`${repoPath(id)}/notes`, { content }, { 'If-Match': `"${etag}"` }),
  /** Every change to the notes, newest first. */
  history: async (id: number): Promise<NotesChange[]> => (await api.get<List<NotesChange>>(`${repoPath(id)}/notes/history`)).items ?? [],
  reindex: (id: number) => api.post<KnowledgeRepo>(`${repoPath(id)}/reindex`),
  /** Reads connected repositories into the viewer's own workspace (indexes are per person). */
  readConnected: (repos: string[]) => api.post<{ repos: string[] }>('/api/github/repos/read', { repos }),
  /** Drops the repository's index, notes and notes history. */
  stop: (id: number) => api.del(repoPath(id)),
  search: async (q: string, opts: { repo?: number; limit?: number } = {}) =>
    (await api.get<{ items: KnowledgeHit[] | null }>(`/api/knowledge/search${qs({ q, repo_id: opts.repo, limit: opts.limit })}`)).items ?? [],
};

/**
 * What Knowledge needs to know about GitHub for its empty states (§9.1):
 * whether it is connected (admins only; null for members, who can't ask)
 * and which repositories Bothy may read. The GitHub API itself is C8's.
 */
export async function githubState(admin: boolean): Promise<{ connected: boolean | null; allowed: string[] }> {
  const [conns, repos] = await Promise.all([
    admin ? api.get<unknown[] | null>('/api/github/connections').catch(() => []) : Promise.resolve(null),
    api.get<{ owner: string; name: string; allowed: boolean }[] | null>('/api/github/repos').catch(() => []),
  ]);
  return {
    connected: conns === null ? null : (conns ?? []).length > 0,
    allowed: (repos ?? []).filter((r) => r.allowed).map((r) => `${r.owner}/${r.name}`.toLowerCase()),
  };
}

/** A repository Bothy may read but hasn't indexed yet, shown as waiting to be read. */
export function queuedRepo(repo: string): KnowledgeRepo {
  const zero = '0001-01-01T00:00:00Z';
  return {
    id: 0,
    principal_id: '',
    repo,
    source: 'github',
    dir: '',
    sha: '',
    indexed_sha: '',
    status: 'queued',
    files: 0,
    chunks: 0,
    bytes: 0,
    skipped: 0,
    truncated: false,
    indexed_at: zero,
    notes: { status: 'none', untrusted: false, updated_at: zero },
    created_at: zero,
    updated_at: zero,
    summary: '',
  };
}

/** A memory document's address: what Bothy remembers for everyone has its own, since {file} never takes TEAM. */
export const memoryUrl = (file: string) => (file === 'TEAM' ? '/api/memory/team' : `/api/memory/${file}`);

export const isConflict = (e: unknown) => e instanceof ApiError && (e.status === 409 || e.status === 412);

/** What the project list says about a repository (§9.1). */
export type RepoState = 'ready' | 'reading' | 'stale' | 'failed';

export function repoState(r: Pick<KnowledgeRepo, 'status' | 'stale'>): RepoState {
  if (r.status === 'error') return 'failed';
  if (r.status === 'queued' || r.status === 'indexing') return 'reading';
  return r.stale ? 'stale' : 'ready';
}

/** Whole percent read, or null when the server doesn't say. */
export function progressPct(r: Pick<KnowledgeRepo, 'progress'>): number | null {
  const p = r.progress;
  if (!p || p.total <= 0) return null;
  return Math.min(99, Math.floor((p.done / p.total) * 100));
}

/**
 * Why a read failed, for what to offer: a repository too big to read fails
 * the same way every time, so only temporary reasons (GitHub limiting
 * requests, or out of reach) get "Read again".
 */
export function failKind(r: Pick<KnowledgeRepo, 'error' | 'truncated'>): 'size' | 'temporary' | 'other' {
  const e = (r.error ?? '').toLowerCase();
  if (/more than [\d,.]+ files/.test(e) || r.truncated || /max_files|too (many|large)|file (count|cap)/.test(e)) return 'size';
  if (/rate.?limit|timeout|unreachable|connection|dial/.test(e)) return 'temporary';
  return 'other';
}

/** "more than 50,000 files" from a size failure, or ''. */
export function fileCap(r: Pick<KnowledgeRepo, 'error'>): string {
  const cap = Number(/more than ([\d,.]+) files/.exec((r.error ?? '').toLowerCase())?.[1]?.replace(/[,.]/g, ''));
  return cap > 0 ? `more than ${cap.toLocaleString()} files` : '';
}

/** The plain reason a read failed; the raw message stays behind Details. */
export function failReason(r: Pick<KnowledgeRepo, 'error' | 'truncated'>): string {
  const e = (r.error ?? '').toLowerCase();
  // The file cap is a server setting, so the number comes from the message or isn't given.
  const cap = Number(/more than ([\d,.]+) files/.exec(e)?.[1]?.replace(/[,.]/g, ''));
  if (cap > 0) return `Too large to read (more than ${cap.toLocaleString()} files).`;
  if (r.truncated || /max_files|too (many|large)|file (count|cap)/.test(e)) return 'Too large to read.';
  if (/rate.?limit/.test(e)) return 'GitHub is limiting how often Bothy can read. Try again later.';
  if (/\b(401|403|404)\b|not found|no access|access denied|forbidden|bad credentials/.test(e)) return 'GitHub says Bothy no longer has access to this repository.';
  if (/timeout|unreachable|connection|dial/.test(e)) return "Bothy couldn't reach GitHub. It will try again later.";
  return 'Something went wrong while reading it.';
}

/** A Markdown snippet as one line of plain text: no list markers, emphasis or code ticks. */
export function plainText(md: string): string {
  return md
    .replace(/!?\[([^\]]*)\]\([^)]*\)/g, '$1')
    .replace(/(^|\s)([-*+]|\d+\.|#+|>)\s+/g, '$1')
    .replace(/[`*_~]/g, '')
    .replace(/\s+/g, ' ')
    .trim();
}

export const byteLength = (s: string) => new TextEncoder().encode(s).length;

/** 'near' above 80 % of the cap (§9.2, §9.4), 'over' past it. */
export function fullness(content: string, cap: number): 'ok' | 'near' | 'over' {
  if (!cap) return 'ok';
  const n = byteLength(content);
  if (n > cap) return 'over';
  return n > cap * 0.8 ? 'near' : 'ok';
}

/** The words a search highlights: two characters or more, without duplicates. */
export function queryWords(q: string): string[] {
  return [...new Set(q.toLowerCase().split(/[^\p{L}\p{N}_]+/u).filter((w) => w.length >= 2))];
}

export type Mark = { text: string; hit: boolean };

/** Splits text into plain and matching pieces, case-insensitively, for <mark>. */
export function marks(text: string, words: string[]): Mark[] {
  if (!words.length || !text) return [{ text, hit: false }];
  const re = new RegExp(`(${words.map((w) => w.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')).join('|')})`, 'giu');
  return text
    .split(re)
    .filter((t) => t !== '')
    .map((t) => ({ text: t, hit: words.includes(t.toLowerCase()) }));
}

/** "line 10" or "lines 10–42", never "#L10-L42" (§9.3). */
export function linesLabel(a: number, b: number): string {
  return b > a ? `lines ${a}–${b}` : `line ${a}`;
}

/** A link to GitHub: the repository, or a file at a commit with its lines. */
export function githubUrl(repo: string, at?: { sha: string; path: string; start: number; end: number }): string {
  const base = `https://github.com/${repo}`;
  if (!at) return base;
  const path = at.path.split('/').map(encodeURIComponent).join('/');
  return `${base}/blob/${at.sha || 'HEAD'}/${path}#L${at.start}${at.end > at.start ? `-L${at.end}` : ''}`;
}

/** Splits "owner/name"; the route is /knowledge/projects/:owner/:repo. */
export const projectHref = (repo: string) => `/knowledge/projects/${repo.split('/').map(encodeURIComponent).join('/')}`;
