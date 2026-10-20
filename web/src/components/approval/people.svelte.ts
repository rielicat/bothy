// Names for the user ids on approvals and chats, until the server sends names
// next to them (S8). Only admins may list people; members know only themselves.
import { api } from '../../lib/api/client';
import type { User } from '../../lib/api/types.gen';
import { session } from '../../lib/stores/session.svelte';

class People {
  names: Record<string, string> = $state({});
  #loading: Promise<void> | null = null;
  #asked = false;

  load() {
    if (!session.isAdmin) return;
    this.#asked = true;
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

  /** The person's display name, '' while unknown. The first call asks for everyone's. */
  known(id: string | undefined): string {
    if (!id) return '';
    const me = session.me?.user;
    if (me && id === me.id) return me.display_name || me.username;
    if (!this.#asked) this.load();
    return this.names[id] ?? '';
  }
}

export const people = new People();
