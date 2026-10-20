import { render, screen } from '@testing-library/svelte';
import { expect, it } from 'vitest';
import FailureLine from './FailureLine.svelte';

it('says what went wrong as an alert with the raw error behind Details, and nothing while it worked', async () => {
  const { container, rerender } = render(FailureLine, { failure: { message: "GitHub didn't answer.", details: 'dial tcp: i/o timeout' } });
  expect(screen.getByRole('alert').textContent).toContain("GitHub didn't answer.");
  expect(screen.getByRole('button', { name: 'Details' })).toBeTruthy();
  await rerender({ failure: null });
  expect(container.textContent?.trim()).toBe('');
});
