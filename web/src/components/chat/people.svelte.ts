// Names for the user ids on approvals and chats. Admins can list everyone;
// members know only themselves until the server sends names with ids (S8).
import { api } from '../../lib/api/client';
import type { User } from '../../lib/api/types.gen';
import { session } from '../../lib/stores/session.svelte';

class People {
  names: Record<string, string> = $state({});
  #loading: Promise<void> | null = null;

  load() {
    if (!session.isAdmin) return;
    this.#loading ??= api
      .get<User[]>('/api/users')
      .then((us) => {
        for (const u of us ?? []) this.names[u.id] = u.display_name || u.username;
      })
      .catch(() => {
        this.#loading = null;
      });
  }

  /** "You" for the viewer, the person's name, else "Someone". */
  name(id: string | undefined, opts: { you?: boolean } = {}): string {
    const me = session.me?.user;
    if (id && id === me?.id) return opts.you === false ? me.display_name || me.username : 'You';
    return (id && this.names[id]) || 'Someone';
  }
}

export const people = new People();

export { channelName as appName } from '../../lib/labels';
