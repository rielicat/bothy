import { api, ApiError, errorText } from '../../../lib/api/client';
import type { VaultStatus } from '../../../lib/api/types.gen';
import { session } from '../../../lib/stores/session.svelte';

export type UnlockResult = { ok: true; status: VaultStatus } | { ok: false; error: string; details?: string };

/** Unlocks the saved keys with the passphrase; the vault answers 403 to a wrong one. */
export async function unlock(passphrase: string): Promise<UnlockResult> {
  if (!passphrase) return { ok: false, error: 'Enter the passphrase.' };
  try {
    const status = await api.post<VaultStatus>('/api/vault/unseal', { passphrase });
    // The server clears the banner on its stream too; this drops it at once.
    session.setBanner({ level: 'error', code: 'vault_sealed', text: '', clear: true });
    return { ok: true, status };
  } catch (e) {
    if (e instanceof ApiError && e.status === 403) return { ok: false, error: "That passphrase didn't work. Check it and try again." };
    return { ok: false, error: "Couldn't unlock the saved keys. Try again.", details: errorText(e) };
  }
}
