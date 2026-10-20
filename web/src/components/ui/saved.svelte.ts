// The page-level "Changes saved" (§11.1): any settings save marks it, and a
// PageHeader with `saves` shows it for 2 s. While no such header is in view,
// the row that saved says "Saved" itself, so one signal is always on screen.
export const saved = $state({ at: 0, header: false });

export function markSaved() {
  saved.at = Date.now();
}

/** What a row says beside its label while and after it saves. */
export const rowStatus = (status: 'saving' | 'saved' | null | undefined) => (status === 'saving' ? 'Saving…' : status === 'saved' && !saved.header ? 'Saved' : '');
