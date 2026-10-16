import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { beforeEach, describe, expect, it } from 'vitest';
import { router } from '../../lib/router.svelte';
import { session } from '../../lib/stores/session.svelte';
import { installApi, type Call } from '../../test/api';
import ScheduleForm from './ScheduleForm.svelte';

let calls: Call[];

beforeEach(async () => {
  calls = installApi();
  session.ready = false;
  await session.init();
});

const posted = () => calls.filter((c) => c.method === 'POST' && c.url === '/api/schedules');

describe('New schedule (docs/design/ui.md §10.3)', () => {
  beforeEach(async () => {
    router.navigate('/activity/schedules/new');
    render(ScheduleForm, { params: { id: 'new' } });
    await screen.findByLabelText('What should Bothy do?');
  });

  it('asks for the task before saving', async () => {
    await fireEvent.click(screen.getByRole('button', { name: 'Create schedule' }));
    expect(await screen.findByText('Tell Bothy what to do.')).toBeTruthy();
    expect(screen.getByLabelText('What should Bothy do?').getAttribute('aria-invalid')).toBe('true');
    expect(posted()).toHaveLength(0);
  });

  it('saves a weekday schedule as a cron rule, named from the prompt', async () => {
    await fireEvent.input(screen.getByLabelText('What should Bothy do?'), { target: { value: "Summarise yesterday's merged pull requests in acme/web and post them to #eng." } });
    expect((screen.getByLabelText('Name') as HTMLInputElement).value).toBe("Summarise yesterday's merged pull requests");
    await fireEvent.change(screen.getByLabelText('Time'), { target: { value: '08:30' } });
    await fireEvent.click(screen.getByRole('button', { name: 'Create schedule' }));
    await waitFor(() => expect(posted()).toHaveLength(1));
    const { schedule } = JSON.parse(posted()[0].body);
    expect(schedule).toMatchObject({ kind: 'cron', spec: '30 8 * * 1-5', name: "Summarise yesterday's merged pull requests" });
    await waitFor(() => expect(router.path).toBe('/activity/schedules'));
  });

  it('shows the repeat rule only under Custom and explains a bad one', async () => {
    expect(screen.queryByLabelText('Repeat rule')).toBeNull();
    await fireEvent.change(screen.getByLabelText('Repeat'), { target: { value: 'custom' } });
    await fireEvent.input(screen.getByLabelText('Repeat rule'), { target: { value: 'every monday' } });
    await fireEvent.input(screen.getByLabelText('What should Bothy do?'), { target: { value: 'Post the digest.' } });
    await fireEvent.click(screen.getByRole('button', { name: 'Create schedule' }));
    expect(await screen.findByText("That repeat rule isn't valid. Example: 0 9 * * 1-5 means weekdays at 9:00.")).toBeTruthy();
    expect(posted()).toHaveLength(0);
  });

  it('moves focus to the first field that needs fixing', async () => {
    await fireEvent.click(screen.getByRole('button', { name: 'Create schedule' }));
    await waitFor(() => expect(document.activeElement).toBe(screen.getByLabelText('What should Bothy do?')));
  });

  it('offers websites the task names, unticked', async () => {
    await fireEvent.input(screen.getByLabelText('What should Bothy do?'), { target: { value: 'Summarise the open pull requests and check status.example.com.' } });
    await fireEvent.click(screen.getByRole('button', { name: 'More options' }));
    const read = (await screen.findByLabelText('Read websites on status.example.com')) as HTMLInputElement;
    expect(read.checked).toBe(false);
    await fireEvent.click(screen.getByRole('button', { name: 'Create schedule' }));
    await waitFor(() => expect(posted()).toHaveLength(1));
    expect(JSON.parse(posted()[0].body).grants).toEqual([]);
  });

  it('keeps results here, the only place an account has', async () => {
    expect(screen.getByText('Send results to')).toBeTruthy();
    expect(screen.getByText('Only here in Bothy')).toBeTruthy();
    expect(screen.queryByRole('combobox', { name: 'Send results to' })).toBeNull();
    expect(calls.some((c) => c.url.startsWith('/api/identities') || c.url.startsWith('/api/spaces'))).toBe(false);
    await fireEvent.input(screen.getByLabelText('What should Bothy do?'), { target: { value: 'Summarise the open pull requests in acme/web.' } });
    await fireEvent.click(screen.getByRole('button', { name: 'Create schedule' }));
    await waitFor(() => expect(posted()).toHaveLength(1));
    expect(JSON.parse(posted()[0].body).schedule.deliver).toMatchObject({ channel: 'web' });
  });

  it("never shows the previous choice's next dates", async () => {
    const line = () => screen.getByText(/^Runs /).textContent ?? '';
    await waitFor(() => expect(line()).toMatch(/Next: /));
    await fireEvent.change(screen.getByLabelText('Repeat'), { target: { value: 'weekly' } });
    expect(line()).toMatch(/^Runs Mondays at .+\)\.$/);
    await waitFor(() => expect(line()).toMatch(/Next: /));
  });

  it('saves a one-off run as wall-clock time in the schedule time zone', async () => {
    await fireEvent.change(screen.getByLabelText('Repeat'), { target: { value: 'once' } });
    await fireEvent.input(screen.getByLabelText('Date'), { target: { value: '2026-11-02' } });
    await fireEvent.input(screen.getByLabelText('What should Bothy do?'), { target: { value: 'Remind the team about the launch.' } });
    await fireEvent.click(screen.getByRole('button', { name: 'Create schedule' }));
    await waitFor(() => expect(posted()).toHaveLength(1));
    expect(JSON.parse(posted()[0].body).schedule).toMatchObject({ kind: 'once', spec: '2026-11-02T09:00', tz: 'Europe/London' });
  });
});

describe('Edit schedule', () => {
  it('opens a rule that fits no choice as Custom and lists recent runs', async () => {
    router.navigate('/activity/schedules/sch_01jinvoices0000000000000');
    render(ScheduleForm, { params: { id: 'sch_01jinvoices0000000000000' } });
    expect(((await screen.findByLabelText('Repeat rule')) as HTMLInputElement).value).toBe('0 9 1,15 * *');
    expect(screen.getByRole('heading', { level: 1 }).textContent).toBe('Invoice reminder');
    expect(screen.getByRole('button', { name: 'Save schedule' })).toBeTruthy();
  });

  it('keeps the check-in on its own form', async () => {
    router.navigate('/activity/schedules/checkin');
    render(ScheduleForm, { params: { id: 'checkin' } });
    expect(await screen.findByLabelText('What to check')).toBeTruthy();
    expect(screen.getByText('Bothy looks through this list regularly and messages you only when something needs attention.')).toBeTruthy();
    expect(await screen.findByText(/a day with Claude Haiku 4\.5\.$/)).toBeTruthy();
  });
});
