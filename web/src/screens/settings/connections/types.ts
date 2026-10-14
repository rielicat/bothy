// Shapes Connections reads, from the generated server types where they exist.
// Workflow and SearchCheck are requests to the server tracks (docs/design/ui.md §17).
export type {
  GitHubConnection,
  GitHubConnectionDetail,
  GitHubInstallation,
  GitHubProbe,
  GitHubRepo,
  GitHubRepoAccess,
  IndexedRepo,
  ReleaseApprover,
  ReleaseEnvironment,
  Releases,
} from '../../../lib/api/types.gen';

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
