// The repositories Bothy reads. Kept apart from knowledge.ts because the
// new-chat screen needs only this, and knowledge.ts belongs to the Knowledge chunk.
import { api, ApiError } from './client';
import type { KnowledgeRepo } from './knowledge';

export const listRepos = async () => (await api.get<{ items: KnowledgeRepo[] | null }>('/api/knowledge/repos')).items ?? [];

/** The server has no Knowledge API yet (S18): the screens show the empty state, not an error. */
export const isUnavailable = (e: unknown) => e instanceof ApiError && (e.status === 404 || e.status === 501 || e.status === 503);

/** The repositories Bothy reads, or none when the server can't say. */
export const readRepos = () => listRepos().catch((e) => (isUnavailable(e) ? [] : Promise.reject(e)));

/** Repositories allowed on any GitHub connection, owner/name; none when GitHub isn't connected. */
export const connectedRepos = () =>
  api
    .get<{ owner: string; name: string }[] | null>('/api/github/repos')
    .then((rs) => (rs ?? []).map((r) => `${r.owner}/${r.name}`))
    .catch(() => [] as string[]);
