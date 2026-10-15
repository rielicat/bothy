// Refine progress for the live run (run.refine, §14.3): which phase the
// drafts are in, in words, instead of a bare "Working…".
import type { RefineData } from '../../lib/api/types.gen';

export interface RefineProgress {
  run: string;
  drafts: number;
  written: number;
  checking: boolean;
  done: boolean;
}

/** Folds one run.refine event into the progress of its run. */
export function refineStep(p: RefineProgress | null, run: string, d: RefineData): RefineProgress | null {
  if (d.phase === 'start') return { run, drafts: d.drafts ?? 0, written: 0, checking: false, done: false };
  if (!p || p.run !== run) return p;
  if (d.phase === 'done') return { ...p, done: true };
  const e = d.entry;
  if (!e) return p;
  return e.round === 0 ? { ...p, written: p.written + 1 } : { ...p, checking: true };
}

export function refineLabel(p: RefineProgress): string {
  if (p.done) return 'Writing the final answer…';
  if (p.checking || (p.drafts > 0 && p.written >= p.drafts)) return 'Checking the drafts…';
  return p.drafts > 1 ? `Writing drafts (${Math.min(p.written + 1, p.drafts)} of ${p.drafts})…` : 'Writing a draft…';
}
