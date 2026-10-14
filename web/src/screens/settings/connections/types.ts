// Local copies of server types this tree's types.gen.ts doesn't have yet.
// The GitHub, releases and knowledge shapes match internal/core and
// internal/httpapi/dto on main (F4–F6); C10 swaps these imports for
// lib/api/types.gen once it is regenerated. The ones marked "not on the
// server yet" are requests to the server tracks (docs/design/ui.md §17).

export interface GitHubConnection {
  id: string;
  auth: 'pat' | 'app' | string;
  account: string;
  account_type: string;
  app_id?: string;
  app_slug?: string;
  installation_id?: number;
  status: string; // ok | error
  error?: string;
  checked_at: string;
  created_at: string;
}

export interface GitHubRepo {
  connection_id: string;
  owner: string;
  name: string;
  default_branch: string;
  private: boolean;
  allowed: boolean;
}

export interface GitHubRepoAccess {
  owner: string;
  name: string;
  private: boolean;
  archived: boolean;
  default_branch: string;
  description?: string;
  allowed: boolean;
  connection_id?: string;
}

export interface GitHubInstallation {
  id: number;
  account: string;
  account_type: string;
}

export interface GitHubProbe {
  ok: boolean;
  error?: string;
  auth: string;
  account: string;
  app_slug?: string;
  installation_id?: number;
  installations?: GitHubInstallation[];
  repos: GitHubRepoAccess[];
  truncated: boolean;
  rate_limit: { limit: number; remaining: number; reset: string };
}

export interface GitHubConnectionDetail {
  connection: GitHubConnection;
  repos: GitHubRepo[];
}

export interface ReleaseApprover {
  kind: 'user' | 'role' | string;
  id: string;
  name: string;
}

export interface ReleaseEnvironment {
  name: string;
  repo: string;
  workflow: string;
  ref_pattern: string;
  inputs: Record<string, string> | null;
  required: number;
  approvers: ReleaseApprover[];
}

export interface Releases {
  environments: ReleaseEnvironment[];
}

/** The part of core.IndexedRepo this page reads (GET /api/knowledge/repos). */
export interface IndexedRepo {
  id: number;
  repo: string; // owner/name, lowercase
  status: string; // queued | indexing | ready | error
  indexed_at: string;
}

/** A repository's GitHub Actions workflow (S19, not on the server yet). */
export interface Workflow {
  name: string;
  path: string;
}

/** POST /api/search/test (not on the server yet): the web search check. */
export interface SearchCheck {
  ok: boolean;
  code?: string;
  error?: string;
}
