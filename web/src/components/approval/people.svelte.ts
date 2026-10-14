// Display names for the user ids on approvals, until the server sends names
// next to them (S8). Only admins may list people; members see "Someone" for
// anyone but themselves.
import { api } from '../../lib/api/client';
import type { User } from '../../lib/api/types.gen';
import { session } from '../../lib/stores/session.svelte';

class People {
  names: Record<string, string> = $state({});
  #asked = false;

  /** The person's display name, '' while unknown. */
  name(id: string | undefined): string {
    if (!id) return '';
    const me = session.me?.user;
    if (me && id === me.id) return me.display_name || me.username;
    if (!this.#asked && session.isAdmin) {
      this.#asked = true;
      void api.get<User[]>('/api/users').then(
        (us) => (this.names = Object.fromEntries(us.map((u) => [u.id, u.display_name || u.username]))),
        () => {},
      );
    }
    return this.names[id] ?? '';
  }

  reset() {
    this.names = {};
    this.#asked = false;
  }
}

export const people = new People();
