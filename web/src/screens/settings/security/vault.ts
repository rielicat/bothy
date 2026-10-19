import { api, ApiError, errorText } from '../../../lib/api/client';
import type { VaultStatus } from '../../../lib/api/types.gen';
import { session } from '../../../lib/stores/session.svelte';

export type UnlockResult = { ok: true; status: VaultStatus } | { ok: false; error: string; details?: string };

/** What a vault with no passphrase yet (VaultStatus.new_passphrase) says above its two fields. */
export const CHOOSE = 'Bothy keeps its saved keys locked with a passphrase. Choose one of at least 12 characters and keep it safe: without it, nobody can unlock them.';

/**
 * Unlocks the saved keys with the passphrase; the vault answers 403 to a wrong one. The first unlock sets the
 * passphrase from one field, so a first passphrase comes with `again`, its second typing, and is checked here.
 */
export async function unlock(passphrase: string, again?: string): Promise<UnlockResult> {
  if (!passphrase) return { ok: false, error: 'Enter the passphrase.' };
  if (again !== undefined && [...passphrase].length < 12) return { ok: false, error: 'Use at least 12 characters.' };
  if (again !== undefined && again !== passphrase) return { ok: false, error: "The two passphrases don't match." };
  try {
    const status = await api.post<VaultStatus>('/api/vault/unseal', { passphrase });
    // The server clears the banner on its stream too; this drops it at once.
    session.setBanner({ level: 'error', code: 'vault_sealed', text: '', clear: true });
    return { ok: true, status };
  } catch (e) {
    if (e instanceof ApiError && e.status === 403) return { ok: false, error: "That passphrase didn't work. Check it and try again." };
    // A key file that doesn't match, or a first passphrase too short: trying again won't help.
    if (e instanceof ApiError && e.status === 400) return { ok: false, error: e.message };
    return { ok: false, error: "Couldn't unlock the saved keys. Try again.", details: errorText(e) };
  }
}
