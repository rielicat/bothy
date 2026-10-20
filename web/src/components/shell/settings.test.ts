import { expect, it } from 'vitest';
import { settingsSections } from './settings';

it('lists the Workspace groups under their heading for admins only', () => {
  expect(settingsSections(false).map((s) => s.label)).toEqual(['You']);
  const admin = settingsSections(true);
  expect(admin.map((s) => s.label)).toEqual(['You', 'Workspace']);
  expect(admin[1].groups.map((g) => g.label)).toEqual(['General', 'Models', 'Connections', 'People', 'Security', 'Advanced']);
});
