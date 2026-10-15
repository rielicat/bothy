import { describe, expect, it } from 'vitest';
import type { RefineData } from '../../lib/api/types.gen';
import { refineLabel, refineStep, type RefineProgress } from './refine';

const entry = (round: number) => ({ phase: 'entry', entry: { draft: 0, round, role: round ? 'critique' : 'draft', text: '', cost_micros: 0 } }) as RefineData;

describe('refine progress', () => {
  it('says which phase the drafts are in', () => {
    let p: RefineProgress | null = refineStep(null, 'run_1', { phase: 'start', drafts: 3, depth: 2 });
    expect(refineLabel(p!)).toBe('Writing drafts (1 of 3)…');
    p = refineStep(p, 'run_1', entry(0));
    expect(refineLabel(p!)).toBe('Writing drafts (2 of 3)…');
    p = refineStep(refineStep(p, 'run_1', entry(0)), 'run_1', entry(0));
    expect(refineLabel(p!)).toBe('Checking the drafts…');
    p = refineStep(p, 'run_1', { phase: 'done' });
    expect(refineLabel(p!)).toBe('Writing the final answer…');
  });

  it("ignores another run's events", () => {
    const p = refineStep(null, 'run_1', { phase: 'start', drafts: 3 });
    expect(refineStep(p, 'run_2', entry(0))).toBe(p);
  });
});
