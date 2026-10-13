import { describe, expect, it } from 'vitest';
import { appendNew } from './list';

describe('appendNew', () => {
  it('appends only rows not already shown, in page order', () => {
    expect(appendNew([{ id: 1 }, { id: 2 }], [{ id: 2 }, { id: 3 }], (x) => x.id)).toEqual([{ id: 1 }, { id: 2 }, { id: 3 }]);
    expect(appendNew([{ id: 1 }], null, (x) => x.id)).toEqual([{ id: 1 }]);
  });
});
