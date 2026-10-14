// Knowledge (C5): repositories Bothy reads, project notes and their history,
// knowledge search, memory files and skills. The repository, notes and search
// routes follow internal/httpapi/h_knowledge.go and h_uistate.go (reading
// progress, out of date, summary, notes history).
import type { KnowledgeHit, KnowledgeRepo, NotesChange } from '../../src/lib/api/knowledge.ts';
import type { Approval, MemoryEvent, SkillMeta } from '../../src/lib/api/types.gen.ts';
import { state as approvalState, waiting } from './approvals.ts';
import { ago, ahead, body, clone, core, emit, fail, json, newId, nowISO, on, onReset, owner, scenario, viewer, type Req } from './core.ts';

// ---- memory ----

export const memory: Record<string, string> = {
  USER: 'Sam runs engineering at Acme and looks after the release process.\n\nPrefers short answers with links to the code.',
  MEMORY:
    '- The v2.3 release freeze starts on Thursdays at 14:00.\n- Staging is reset every Monday morning.\n- Customer-facing changes need a line in the release notes.\n- The design team reviews anything that changes the sign-in pages.',
  HEARTBEAT: '- [ ] Tell me if a deploy to production failed overnight\n- [ ] Remind me to send the release notes on Fridays',
};

/** MemoryEvent plus the S8 names the history shows instead of ids. */
type MemoryChange = MemoryEvent & { actor_name?: string; chat?: { id: string; title: string } };

export const memoryHistory: MemoryChange[] = [
  {
    id: 41,
    principal_id: owner.id,
    file: 'MEMORY',
    op: 'add',
    before: '- The v2.3 release freeze starts on Thursdays at 14:00.\n- Staging is reset every Monday morning.\n- Customer-facing changes need a line in the release notes.',
    after: memory.MEMORY,
    run_id: 'run_01jlogin00000000000000000',
    actor: 'agent:run_01jlogin00000000000000000',
    provenance: 'user',
    at: ago(60 * 3),
    chat: { id: 'ses_01jlogin00000000000000000', title: 'Fix login redirect' },
  },
  {
    id: 40,
    principal_id: owner.id,
    file: 'USER',
    op: 'edit',
    before: 'Sam runs engineering at Acme.',
    after: memory.USER,
    actor: `user:${owner.id}`,
    provenance: 'user',
    at: ago(60 * 24 * 2),
  },
  {
    id: 39,
    principal_id: owner.id,
    file: 'MEMORY',
    op: 'edit',
    before: '- The release freeze starts on Thursdays.\n- Staging is reset every Monday morning.\n- Customer-facing changes need a line in the release notes.',
    after: '- The v2.3 release freeze starts on Thursdays at 14:00.\n- Staging is reset every Monday morning.\n- Customer-facing changes need a line in the release notes.',
    actor: 'user:usr_01jpriya000000000000000000',
    actor_name: 'Priya Rao',
    provenance: 'user',
    at: ago(60 * 24 * 6),
  },
];

const memEmpty = scenario('memory-empty', 'Bothy has nothing in About you or Things Bothy remembers yet.', 'knowledge');
const memFull = scenario('memory-near-full', 'Things Bothy remembers is above 80 % of its size limit.', 'knowledge');

// Near full: the seeded text fills 85 % of the cap, so a few more lines go over it.
const nearFull = (s: string) => Math.ceil(new TextEncoder().encode(s).length / 0.85);
const memCap = (f: string, req?: Req) => (f === 'MEMORY' ? (memFull(req) ? nearFull(memory.MEMORY) : 4096) : 2048);

// ---- skills ----

export const skills: SkillMeta[] = [
  {
    name: 'release-notes',
    description: 'Writes release notes from the merged pull requests since the last tag.',
    path: 'skills/release-notes',
    hash: 'h1',
    approved_hash: 'h1',
    status: 'enabled',
    tokens: 64,
    installed_at: ago(60 * 24 * 20),
    updated_at: ago(60 * 24 * 20),
  },
  {
    name: 'incident-review',
    description: 'Drafts a blameless incident review from a chat or a timeline.',
    path: 'skills/incident-review',
    hash: 'h2b',
    approved_hash: 'h2',
    status: 'changed',
    tokens: 88,
    installed_at: ago(60 * 24 * 30),
    updated_at: ago(60 * 5),
  },
  {
    name: 'onboarding-checklist',
    description: 'Builds a first-week checklist for a new teammate.',
    path: 'skills/onboarding-checklist',
    hash: 'h3',
    approved_hash: 'h3',
    status: 'disabled',
    tokens: 51,
    installed_at: ago(60 * 24 * 9),
    updated_at: ago(60 * 24 * 9),
  },
];

const skillFiles: Record<string, Record<string, string>> = {
  'release-notes': {
    'templates/notes.md': '## {version}\n\n### New\n{features}\n\n### Fixed\n{fixes}\n',
    'scripts/collect.sh': '#!/bin/sh\n# Lists merged pull requests since the last tag.\ngit log --merges --oneline "$(git describe --tags --abbrev=0)"..HEAD\n',
  },
};

const skillInstructions: Record<string, string> = {
  'release-notes':
    'Use this when someone asks for release notes or a changelog.\n\n1. Find the last release tag in the repository.\n2. List the pull requests merged since then and group them as new features and fixes.\n3. Write one plain sentence per change, for customers rather than engineers.\n\nKeep the notes under a page. [Our style guide](https://example.com/style) has the tone.',
  'incident-review':
    'Use this after an outage or a customer-facing bug, when someone asks for an incident review.\n\n1. Build a timeline from the chat, the deploys and the alerts, with times in the team\'s time zone.\n2. Say what customers saw and for how long.\n3. List what made it worse and what helped, without naming anyone as the cause.\n4. End with at most three follow-ups, each with an owner.',
  'onboarding-checklist':
    'Use this when someone joins the team.\n\n1. Ask which team they join and who their buddy is.\n2. List the accounts to request on day one: GitHub, Slack and the staging dashboard.\n3. Add one small first task from the open issues labelled “good first issue”.',
};

const skillBody = (s: SkillMeta) => `---\nname: ${s.name}\ndescription: ${s.description}\n---\n\n# ${s.name}\n\n${skillInstructions[s.name] ?? s.description}`;

const skillsEmpty = scenario('skills-empty', 'No skills are installed.', 'knowledge');

// ---- repositories, notes and search ----

const sha = { web: '4f2c1a9e8b7d6c5f4e3d2c1b0a9f8e7d6c5b4a39', api: '9b1e07c2d4f6a8b0c2e4f6a8b0c2e4f6a8b0c2e4', infra: 'c0ffee1234567890abcdef1234567890abcdef12', docs: '1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b' };

const webNotes = `## What this is

Customer-facing web app, deployed from main. Built with Next.js and talks to acme/api for everything behind the sign-in.

## How to build and test

Run \`pnpm install\`, then \`pnpm test\`. End-to-end tests need a running API: start it with \`pnpm dev:api\` first.

## Conventions

- Components live next to the page that uses them; shared ones go in \`src/ui\`.
- Every change that customers see needs a line in \`CHANGELOG.md\`.

## Important decisions

- Login redirects only go to paths on our own domain, never to a full address (see \`src/auth/redirect.ts\`).
- We stay on the pages router until the v3 redesign.`;

const docsNotes = `## What this is

The public help centre, built from Markdown with Astro.

## How to build and test

Run \`npm ci\`, then \`npm run build\`. Links are checked on every pull request.`;

const zero = '0001-01-01T00:00:00Z';
const repo = (id: number, name: string, extra: Partial<KnowledgeRepo>): KnowledgeRepo => ({
  id,
  principal_id: owner.id,
  repo: name,
  source: 'github',
  dir: `repos/${name}`,
  sha: '',
  indexed_sha: '',
  status: 'ready',
  files: 0,
  chunks: 0,
  bytes: 0,
  skipped: 0,
  truncated: false,
  indexed_at: zero,
  notes: { status: 'none', untrusted: false, updated_at: zero },
  created_at: ago(60 * 24 * 3),
  updated_at: ago(60),
  summary: '',
  ...extra,
});

export const repos: KnowledgeRepo[] = [
  repo(1, 'acme/web', {
    sha: sha.web,
    files: 1204,
    chunks: 5310,
    bytes: 18_400_000,
    indexed_at: ago(60 * 2),
    // The last change in notesHistory below (Priya Rao's edit).
    notes: { status: 'ready', untrusted: false, updated_at: ago(60 * 26) },
    summary: 'Customer-facing web app, deployed from main.',
  }),
  repo(2, 'acme/api', { sha: sha.api, status: 'indexing', files: 480, progress: { done: 480, total: 1200 }, notes: { status: 'drafting', untrusted: false, updated_at: zero }, summary: '' }),
  repo(3, 'acme/infra', { sha: sha.infra, status: 'error', error: 'walk stopped: more than 50000 files (knowledge.max_files)', truncated: true }),
  repo(4, 'acme/docs', { sha: sha.docs, files: 212, chunks: 880, bytes: 2_100_000, indexed_at: ago(60 * 24 * 9), stale: true, notes: { status: 'ready', untrusted: false, updated_at: ago(60 * 24 * 9) }, summary: 'The public help centre, built from Markdown with Astro.' }),
];

const notesByRepo: Record<number, string> = { 1: webNotes, 4: docsNotes };

export const notesHistory: Record<number, NotesChange[]> = {
  1: [
    {
      id: 3,
      actor: 'user',
      name: 'Priya Rao',
      user_id: 'usr_01jpriya000000000000000000',
      before: webNotes.replace('We stay on the pages router until the v3 redesign.', 'We stay on the pages router for now.'),
      after: webNotes,
      at: ago(60 * 26),
    },
    {
      id: 2,
      actor: 'bothy',
      before: webNotes.replace(/\n- Login redirects[^\n]*/, '').replace('We stay on the pages router until the v3 redesign.', 'We stay on the pages router for now.'),
      after: webNotes.replace('We stay on the pages router until the v3 redesign.', 'We stay on the pages router for now.'),
      at: ago(60 * 24 * 2),
      chat: { id: 'ses_01jlogin00000000000000000', title: 'Login bug' },
    },
    { id: 1, actor: 'bothy', before: '', after: webNotes.split('\n## Important decisions')[0], at: ago(60 * 24 * 4) },
  ],
};

/** Indexed text the mock searches: code windows and notes sections. */
interface Chunk {
  repo_id: number;
  path: string;
  start: number;
  lines: string[];
  title: string;
  source: 'code' | 'notes';
}

const code = (repo_id: number, path: string, start: number, text: string, title = ''): Chunk => ({ repo_id, path, start, lines: text.split('\n'), title, source: 'code' });

const corpus: Chunk[] = [
  code(
    1,
    'src/auth/redirect.ts',
    10,
    "export function loginRedirect(url: URL): string {\n  const next = url.searchParams.get('next') ?? '/';\n  return safeRedirect(next);\n}\n\n// Only same-site paths: a full address would send people elsewhere after login.\nexport function safeRedirect(next: string): string {\n  return next.startsWith('/') && !next.startsWith('//') ? next : '/';\n}",
    'loginRedirect',
  ),
  code(1, 'src/pages/login.tsx', 22, "  const router = useRouter();\n  const done = () => router.replace(loginRedirect(new URL(window.location.href)));\n  return <SignInForm onSignedIn={done} />;", 'LoginPage'),
  code(1, 'docs/release.md', 1, '# Releasing acme/web\n\nFreeze main on Thursday at 14:00, then deploy to staging.\nAfter the smoke tests pass, deploy to production from the same commit.', 'Releasing acme/web'),
  code(4, 'src/content/sign-in.md', 5, "If the page keeps sending you back to the login screen after you sign in,\nclear the site's cookies and try again. A redirect loop usually means an old cookie.", 'Trouble signing in'),
];

function notesChunks(repo_id: number, md: string): Chunk[] {
  return md
    .split(/^## /m)
    .filter(Boolean)
    .map((sec) => {
      const [title, ...rest] = sec.split('\n');
      return { repo_id, path: 'NOTES.md', start: 1, lines: [rest.join(' ').replace(/\s+/g, ' ').trim()], title: title.trim(), source: 'notes' as const };
    });
}

const knowledgeEmpty = scenario('knowledge-empty', 'Bothy has read no repositories yet.', 'knowledge');
const notesConflict = scenario('notes-conflict', 'Someone else saves the acme/web notes while you edit them: your first save is refused.', 'knowledge');
const notesFull = scenario('notes-near-full', 'The acme/web notes are above 80 % of their size limit.', 'knowledge');
const notesSuggest = scenario('notes-suggestions', 'Bothy suggested 2 changes to the acme/web notes, from content it read.', 'knowledge');

export const state = {
  memory: {} as Record<string, string>,
  etags: {} as Record<string, string>,
  memoryHistory: [] as MemoryChange[],
  skills: [] as SkillMeta[],
  repos: [] as KnowledgeRepo[],
  notes: {} as Record<number, string>,
  notesEtag: {} as Record<number, string>,
  notesHistory: {} as Record<number, NotesChange[]>,
  conflicted: false,
  suggested: false,
};

const etagOf = (s: string) => {
  let h = 5381;
  for (let i = 0; i < s.length; i++) h = ((h << 5) + h + s.charCodeAt(i)) | 0;
  return `n${(h >>> 0).toString(16)}`;
};

onReset(() => {
  Object.assign(state, {
    memory: clone(memory),
    etags: { MEMORY: 'e1', USER: 'e1', HEARTBEAT: 'e1' },
    memoryHistory: clone(memoryHistory),
    skills: clone(skills),
    repos: clone(repos),
    notes: clone(notesByRepo),
    notesEtag: Object.fromEntries(Object.entries(notesByRepo).map(([k, v]) => [k, etagOf(v)])),
    notesHistory: clone(notesHistory),
    conflicted: false,
    suggested: false,
  });
  // Unit tests switch scenarios on with resetMock({ mock }); pages do it per request below.
  if (notesSuggest()) suggest();
});

// ---- note suggestions: notes approvals in the approvals domain ----

function suggestion(n: number, section: string, line: string, chat: string): Approval {
  const before = state.notes[1] ?? webNotes;
  const after = appendToSection(before, section, line);
  const diff = `--- a/acme/web/NOTES.md\n+++ b/acme/web/NOTES.md\n${unified(before, after)}`;
  const id = `apr_01jnotes${n}000000000000000000`.slice(0, 30);
  const untrusted = 'This request came from content Bothy read (a web page, repository or message), not from a person.';
  return {
    id,
    run_id: `run_01jnotes${n}000000000000000000`.slice(0, 30),
    session_id: 'ses_01jlogin00000000000000000',
    principal_id: owner.id,
    tool_call_id: `tcl_01jnotes${n}00000000000000000`.slice(0, 30),
    action: { tool: 'notes', kind: 'memory', side_effect: 'local_write', payload: { repo: 'acme/web', op: 'append', section, text: line }, diff, fingerprint: `fp${n}` },
    decision: {
      verdict: 'ask',
      rule: 'floor:tainted_memory',
      reason: 'The change came from content Bothy read',
      floor: true,
      approver: { user_id: owner.id, admin_only: false },
      timeout: 60 * 60e9,
      on_timeout: 'park',
      choices: ['allow_once', 'deny'],
      warnings: [untrusted],
    },
    card: { title: 'Update the notes for acme/web?', risk: 'local_write', fields: [], diff, warnings: [untrusted], context: `Web · session “${chat}”`, choices: ['allow_once', 'deny'], expires: ahead(55), web_url: '' },
    status: 'pending',
    approver: { user_id: owner.id, admin_only: false },
    on_timeout: 'park',
    created_at: ago(n * 7),
    expires_at: ahead(55),
  };
}

/** The notes tool's op=append: the text goes at the end of the section. */
function appendToSection(md: string, section: string, text: string): string {
  const start = md.indexOf(`## ${section}\n`);
  if (start < 0) return `${md.trimEnd()}\n\n## ${section}\n\n${text}\n`;
  const next = md.indexOf('\n## ', start + 1);
  const end = next < 0 ? md.length : next;
  return `${md.slice(0, end).trimEnd()}\n${text}\n${md.slice(end)}`;
}

/** A minimal unified hunk for a change that only adds lines. */
function unified(before: string, after: string): string {
  const a = before.split('\n');
  const b = after.split('\n');
  let i = 0;
  while (i < a.length && a[i] === b[i]) i++;
  const added = b.length - a.length;
  const ctxStart = Math.max(0, i - 2);
  const ctx = a.slice(ctxStart, i).map((l) => ` ${l}`);
  const add = b.slice(i, i + added).map((l) => `+${l}`);
  const tail = a.slice(i, i + 2).map((l) => ` ${l}`);
  return `@@ -${ctxStart + 1},${ctx.length + tail.length} +${ctxStart + 1},${ctx.length + add.length + tail.length} @@\n${[...ctx, ...add, ...tail].join('\n')}`;
}

function suggest() {
  if (state.suggested) return;
  state.suggested = true;
  const items = [
    suggestion(1, 'Conventions', '- API errors are shown with the shared `ErrorNotice` component, never with alert().', 'Login bug'),
    suggestion(2, 'How to build and test', 'Set `E2E_BASE_URL` to test against staging instead of a local API.', 'Flaky e2e tests'),
  ];
  for (const a of items) {
    approvalState.approvals = [a, ...approvalState.approvals.filter((x) => x.id !== a.id)];
    waiting.set(a.id, (choice) => {
      if (choice === 'deny') return;
      const p = a.action?.payload as { section: string; text: string };
      const before = state.notes[1] ?? '';
      const after = appendToSection(before, p.section, p.text);
      writeNotes(1, after, { id: Date.now(), actor: 'bothy', before, after, at: nowISO(), chat: { id: a.session_id, title: 'Login bug' } });
    });
    emit('approval.requested', { approval: a }, a.session_id, a.run_id);
  }
}

function writeNotes(id: number, content: string, change: NotesChange) {
  state.notes[id] = content;
  state.notesEtag[id] = etagOf(content);
  (state.notesHistory[id] ??= []).unshift(change);
  const r = state.repos.find((x) => x.id === id);
  if (r) {
    r.notes = { status: content ? 'ready' : 'none', untrusted: false, updated_at: nowISO() };
    emit('knowledge.updated', { repo: r });
  }
}

const notesCap = (req?: Req) => (notesFull(req) ? nearFull(webNotes) : 8192);

function visibleRepos(req: Req): KnowledgeRepo[] {
  if (knowledgeEmpty(req)) return [];
  if (notesSuggest(req)) suggest();
  return state.repos;
}

const findRepo = (req: Req, id: string) => visibleRepos(req).find((r) => r.id === Number(id));

on('GET', '/api/knowledge/repos', (req, res) => json(res, 200, { items: visibleRepos(req) }));
on('GET', '/api/knowledge/repos/{id}', (req, res, m) => {
  const r = findRepo(req, m[1]);
  return r ? json(res, 200, r) : fail(res, 404, 'not_found', 'not found');
});
on('POST', '/api/knowledge/repos/{id}/reindex', (req, res, m) => {
  const r = findRepo(req, m[1]);
  if (!r) return fail(res, 404, 'not_found', 'not found');
  if (viewer(req).role === 'member') return fail(res, 403, 'forbidden', 'admins only');
  Object.assign(r, { status: 'queued', stale: false, error: undefined, truncated: false, progress: { done: 0, total: r.files || 1000 } });
  emit('knowledge.updated', { repo: r });
  json(res, 202, r);
});
on('DELETE', '/api/knowledge/repos/{id}', (req, res, m) => {
  const r = findRepo(req, m[1]);
  if (!r) return fail(res, 404, 'not_found', 'not found');
  if (viewer(req).role === 'member') return fail(res, 403, 'forbidden', 'admins only');
  state.repos = state.repos.filter((x) => x !== r);
  emit('knowledge.updated', { repo: r, deleted: true });
  json(res, 204);
});

const notesDoc = (req: Req, r: KnowledgeRepo) => ({
  repo_id: r.id,
  repo: r.repo,
  content: state.notes[r.id] ?? '',
  etag: state.notes[r.id] ? state.notesEtag[r.id] : '',
  cap_bytes: notesCap(req),
  status: r.notes.status,
  untrusted: r.notes.untrusted,
  updated_at: r.notes.updated_at,
});

on('GET', '/api/knowledge/repos/{id}/notes', (req, res, m) => {
  const r = findRepo(req, m[1]);
  return r ? json(res, 200, notesDoc(req, r)) : fail(res, 404, 'not_found', 'not found');
});
on('PUT', '/api/knowledge/repos/{id}/notes', async (req, res, m) => {
  const r = findRepo(req, m[1]);
  if (!r) return fail(res, 404, 'not_found', 'not found');
  const raw = req.headers['if-match'];
  if (raw === undefined) return fail(res, 428, 'bad_request', 'If-Match with the notes etag is required');
  const match = String(raw).replace(/^"|"$/g, '');
  const { content } = JSON.parse(await body(req)) as { content: string };
  // The first save in this scenario loses a race with Priya's edit.
  if (notesConflict(req) && !state.conflicted && r.id === 1) {
    state.conflicted = true;
    const before = state.notes[1];
    const theirs = before.replace('## Conventions\n\n', '## Conventions\n\n- Use the shared date helpers in `src/lib/dates.ts`; never format dates by hand.\n');
    writeNotes(1, theirs, { id: Date.now(), actor: 'user', name: 'Priya Rao', user_id: 'usr_01jpriya000000000000000000', before, after: theirs, at: nowISO() });
  }
  const cur = state.notes[r.id] ? state.notesEtag[r.id] : '';
  if (match !== cur) return fail(res, 409, 'conflict', 'the notes changed since they were loaded');
  if (new TextEncoder().encode(content).length > notesCap(req)) return fail(res, 400, 'bad_request', `notes are limited to ${notesCap(req)} bytes`);
  const me = viewer(req);
  writeNotes(r.id, content, { id: Date.now(), actor: 'user', name: me.display_name, user_id: me.id, before: state.notes[r.id] ?? '', after: content, at: nowISO() });
  json(res, 200, notesDoc(req, r));
});
on('GET', '/api/knowledge/repos/{id}/notes/history', (req, res, m) => {
  const r = findRepo(req, m[1]);
  return r ? json(res, 200, { items: state.notesHistory[r.id] ?? [] }) : fail(res, 404, 'not_found', 'not found');
});
on('GET', '/api/knowledge/repos/{id}/notes/history/{version}', (req, res, m) => {
  const v = findRepo(req, m[1]) && state.notesHistory[Number(m[1])]?.find((c) => c.id === Number(m[2]));
  return v ? json(res, 200, v) : fail(res, 404, 'not_found', 'not found');
});

on('GET', '/api/knowledge/search', (req, res, _m, q) => {
  const words = (q.get('q') ?? '')
    .toLowerCase()
    .split(/[^\p{L}\p{N}_]+/u)
    .filter((w) => w.length >= 2);
  if (!words.length) return fail(res, 400, 'bad_request', 'q is required');
  const repoId = Number(q.get('repo_id') ?? 0);
  const limit = Math.min(50, Number(q.get('limit') ?? 20) || 20);
  const known = visibleRepos(req);
  const chunks = [...corpus, ...known.flatMap((r) => notesChunks(r.id, state.notes[r.id] ?? ''))];
  const hits: KnowledgeHit[] = [];
  for (const c of chunks) {
    const r = known.find((x) => x.id === c.repo_id);
    if (!r || (repoId && r.id !== repoId)) continue;
    const text = [c.title, ...c.lines].join('\n').toLowerCase();
    const score = words.filter((w) => text.includes(w)).length;
    if (!score) continue;
    const end = c.start + c.lines.length - 1;
    hits.push({
      repo_id: r.id,
      repo: r.repo,
      principal_id: r.principal_id,
      sha: r.sha,
      path: c.path,
      start_line: c.start,
      end_line: end,
      kind: c.source === 'notes' ? 'heading' : 'func',
      lang: c.source === 'notes' ? 'markdown' : 'typescript',
      title: c.title,
      snippet: c.lines.join('\n'),
      citation: `${r.repo}@${r.sha.slice(0, 7)}:${c.path}#L${c.start}-L${end}`,
      rank: -score,
      source: c.source,
    });
  }
  hits.sort((a, b) => a.rank - b.rank);
  json(res, 200, { items: hits.slice(0, limit) });
});

// ---- memory and skills routes ----

const memFile = (f: string) => f.toUpperCase();
const memContent = (req: Req, f: string) => (memEmpty(req) && f !== 'HEARTBEAT' && state.etags[f] === 'e1' ? '' : (state.memory[f] ?? ''));
on('GET', '/api/memory/export', (_q, res) => {
  res.setHeader('Content-Disposition', 'attachment; filename="memory.tar"');
  res.end('mock tar');
});
on('GET', '/api/memory/{file}', (req, res, m) => {
  const f = memFile(m[1]);
  json(res, 200, { file: f, content: memContent(req, f), etag: state.etags[f], cap_bytes: memCap(f, req) });
});
on('PUT', '/api/memory/{file}', async (req, res, m) => {
  const f = memFile(m[1]);
  if (String(req.headers['if-match'] ?? '').replace(/^"|"$/g, '') !== state.etags[f]) return fail(res, 409, 'conflict', 'The file changed since you loaded it.');
  const { content } = JSON.parse(await body(req)) as { content: string };
  if (new TextEncoder().encode(content).length > memCap(f, req)) return fail(res, 400, 'bad_request', `limited to ${memCap(f, req)} bytes`);
  state.memoryHistory.unshift({ id: Math.floor(Math.random() * 1e6), principal_id: core.me.id, file: f, op: 'edit', before: memContent(req, f), after: content, actor: `user:${viewer(req).id}`, provenance: 'user', at: nowISO() });
  state.memory[f] = content;
  state.etags[f] = newId('e').slice(0, 8);
  json(res, 200, { file: f, content, etag: state.etags[f], cap_bytes: memCap(f, req) });
});
on('GET', '/api/memory/{file}/history', (req, res, m) => json(res, 200, { items: memEmpty(req) ? [] : state.memoryHistory.filter((e) => e.file === memFile(m[1])) }));

on('GET', '/api/skills', (req, res) => json(res, 200, skillsEmpty(req) ? [] : state.skills));
on('GET', '/api/skills/{name}', (_q, res, m) => {
  const s = state.skills.find((x) => x.name === m[1]);
  if (!s) return fail(res, 404, 'not_found', 'no such skill');
  json(res, 200, { meta: s, body: skillBody(s), files: ['SKILL.md', ...Object.keys(skillFiles[s.name] ?? {})] });
});
// One file of a skill as plain text, for the skill page's Files list.
on('GET', '/api/skills/{name}/files/{path...}', (_q, res, m) => {
  const text = skillFiles[m[1]]?.[decodeURIComponent(m[2])];
  if (text === undefined) return fail(res, 404, 'not_found', 'no such file');
  res.statusCode = 200;
  res.setHeader('Content-Type', 'text/plain; charset=utf-8');
  res.end(text);
});
on('PATCH', '/api/skills/{name}', async (req, res, m) => {
  const s = state.skills.find((x) => x.name === m[1]);
  if (!s) return fail(res, 404, 'not_found', 'no such skill');
  if (viewer(req).role === 'member') return fail(res, 403, 'forbidden', 'admins only');
  const { enabled, hash } = JSON.parse(await body(req)) as { enabled: boolean; hash?: string };
  if (enabled && hash !== s.hash) return fail(res, 409, 'conflict', 'the skill files changed; review them again');
  s.status = enabled ? 'enabled' : 'disabled';
  if (enabled) s.approved_hash = s.hash;
  json(res, 200, s);
});
on('DELETE', '/api/skills/{name}', (_q, res, m) => {
  state.skills = state.skills.filter((x) => x.name !== m[1]);
  json(res, 204);
});
on('POST', '/api/skills/rescan', (_q, res) => json(res, 200, state.skills));
on('POST', '/api/skills', (_q, res) => {
  const s: SkillMeta = { ...state.skills[0], name: `uploaded-skill-${state.skills.length}`, description: 'Uploaded from a zip file.', hash: 'hu', approved_hash: '', status: 'pending', installed_at: nowISO(), updated_at: nowISO() };
  state.skills.push(s);
  json(res, 200, s);
});
