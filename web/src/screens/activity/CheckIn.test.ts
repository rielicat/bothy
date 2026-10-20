import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { beforeEach, describe, expect, it } from 'vitest';
import { state as mockKnowledge } from '../../../mock/domains/knowledge';
import { router } from '../../lib/router.svelte';
import { session } from '../../lib/stores/session.svelte';
import { installApi, type Call } from '../../test/api';
import CheckIn from './CheckIn.svelte';

let calls: Call[];

beforeEach(async () => {
  calls = installApi();
  session.ready = false;
  await session.init();
});

async function open(path = '/activity/schedules/checkin') {
  router.navigate(path);
  render(CheckIn);
  return (await screen.findByLabelText('What to check')) as HTMLTextAreaElement;
}

const saved = (method: string, url: string) => calls.find((c) => c.method === method && c.url === url);

describe('Check-in (docs/design/ui.md §10.3)', () => {
  it('offers suggested lines while the list is empty, and saves them only on Save', async () => {
    mockKnowledge.memory.HEARTBEAT = '';
    const box = await open();
    const use = await screen.findByRole('button', { name: 'Use suggested lines' });
    await fireEvent.click(use);
    expect(box.value).toBe(
      [
        'Tell me if a deploy to production failed.',
        'Tell me about pull requests in acme/web that have waited more than 2 days for a review.',
        'Tell me if a build on the default branch of acme/web failed.',
      ].join('\n'),
    );
    expect(document.activeElement).toBe(box);
    expect(screen.queryByRole('button', { name: 'Use suggested lines' })).toBeNull();
    expect(saved('PUT', '/api/memory/HEARTBEAT')).toBeUndefined();

    await fireEvent.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(saved('PUT', '/api/memory/HEARTBEAT')).toBeTruthy());
    expect(JSON.parse(saved('PUT', '/api/memory/HEARTBEAT')!.body).content).toBe('- [ ] Tell me if a deploy to production failed.\n- [ ] Tell me about pull requests in acme/web that have waited more than 2 days for a review.\n- [ ] Tell me if a build on the default branch of acme/web failed.\n');
  });

  it('offers them again once the box is cleared, and not over a list someone wrote', async () => {
    const box = await open();
    expect(box.value).toContain('Tell me if a deploy to production failed overnight');
    expect(screen.queryByRole('button', { name: 'Use suggested lines' })).toBeNull();
    await fireEvent.input(box, { target: { value: '  ' } });
    expect(await screen.findByRole('button', { name: 'Use suggested lines' })).toBeTruthy();
  });

  it('saves the check-in on when turning it on opened the form', async () => {
    mockKnowledge.memory.HEARTBEAT = '';
    const box = await open('/activity/schedules/checkin?on=1');
    await fireEvent.input(box, { target: { value: 'Tell me if the nightly backup failed.' } });
    await fireEvent.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(saved('PATCH', '/api/schedules/sch_01jcheckin00000000000000')).toBeTruthy());
    expect(JSON.parse(saved('PATCH', '/api/schedules/sch_01jcheckin00000000000000')!.body).schedule.enabled).toBe(true);
  });

  it("won't turn on a check-in with nothing to check, and points to the suggested lines", async () => {
    mockKnowledge.memory.HEARTBEAT = '';
    const box = await open('/activity/schedules/checkin?on=1');
    await fireEvent.click(screen.getByRole('button', { name: 'Save' }));
    expect(await screen.findByText('Add something to check first.')).toBeTruthy();
    expect(box.getAttribute('aria-invalid')).toBe('true');
    expect(document.activeElement).toBe(box);
    expect(calls.some((c) => c.method !== 'GET')).toBe(false);
    await fireEvent.click(screen.getByRole('button', { name: 'Use suggested lines' }));
    expect(screen.queryByText('Add something to check first.')).toBeNull();
  });

  it('saves an empty list while the check-in stays off', async () => {
    mockKnowledge.memory.HEARTBEAT = '';
    await open();
    await fireEvent.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(saved('PATCH', '/api/schedules/sch_01jcheckin00000000000000')).toBeTruthy());
    expect(screen.queryByText('Add something to check first.')).toBeNull();
  });

  it('leaves an off check-in off when it was opened to edit', async () => {
    await open();
    await fireEvent.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(saved('PATCH', '/api/schedules/sch_01jcheckin00000000000000')).toBeTruthy());
    expect(JSON.parse(saved('PATCH', '/api/schedules/sch_01jcheckin00000000000000')!.body).schedule.enabled).toBe(false);
  });
});
