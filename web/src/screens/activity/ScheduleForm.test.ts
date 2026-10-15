import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { beforeEach, describe, expect, it } from 'vitest';
import type { ChannelIdentity } from '../../lib/api/types.gen';
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

  it('starts with sending to the results chat allowed, keyed the way the server keys chats', async () => {
    await fireEvent.input(screen.getByLabelText('What should Bothy do?'), { target: { value: 'Summarise the open pull requests and check status.example.com.' } });
    await fireEvent.click(screen.getByRole('button', { name: 'More options' }));
    const send = (await screen.findByLabelText('Send you messages on Telegram')) as HTMLInputElement;
    const read = screen.getByLabelText('Read websites on status.example.com') as HTMLInputElement;
    expect(send.checked).toBe(true);
    expect(read.checked).toBe(false);
    await fireEvent.click(screen.getByRole('button', { name: 'Create schedule' }));
    await waitFor(() => expect(posted()).toHaveLength(1));
    expect(JSON.parse(posted()[0].body).grants).toEqual([expect.objectContaining({ kind: 'recipient', value: 'telegram:5512:', id: '' })]);
  });

  it("offers only the person's own chats, and no group chat a personal schedule can't post to", async () => {
    const select = screen.getByLabelText('Send results to') as HTMLSelectElement;
    expect([...select.options].map((o) => o.textContent)).toEqual(['Your Telegram messages', 'Your Slack messages', 'Only here in Bothy']);
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

describe('New schedule without a chat app of your own', () => {
  // An admin is sent everyone's identities; this one belongs to someone else.
  const someoneElses: ChannelIdentity[] = [
    {
      channel: 'telegram',
      external_id: '555001',
      username: 'tester',
      display_name: 'Tester',
      status: 'linked',
      user_id: 'usr_01jtester0000000000000000',
      dm_chat_id: '555001',
      first_seen_at: '2026-10-01T09:00:00Z',
      last_seen_at: '2026-10-14T09:00:00Z',
    },
  ];

  it("starts with results here and never sends to someone else's chat", async () => {
    calls = installApi({ override: (method, url) => (method === 'GET' && url === '/api/identities' ? someoneElses : undefined) });
    session.ready = false;
    await session.init();
    router.navigate('/activity/schedules/new');
    render(ScheduleForm, { params: { id: 'new' } });
    const select = (await screen.findByLabelText('Send results to')) as HTMLSelectElement;
    expect([...select.options].map((o) => o.textContent)).toEqual(['Only here in Bothy']);
    expect(select.value).toBe('web');
    await fireEvent.input(screen.getByLabelText('What should Bothy do?'), { target: { value: 'Summarise the open pull requests in acme/web.' } });
    await fireEvent.click(screen.getByRole('button', { name: 'Create schedule' }));
    await waitFor(() => expect(posted()).toHaveLength(1));
    const body = JSON.parse(posted()[0].body);
    expect(body.schedule.deliver).toMatchObject({ channel: 'web' });
    expect(body.grants).toEqual([]);
  });

  it("doesn't promise a usual chat app that isn't linked", async () => {
    installApi({ override: (method, url) => (method === 'GET' && url === '/api/identities' ? someoneElses : undefined) });
    session.ready = false;
    await session.init();
    router.navigate('/activity/schedules/sch_01jinvoices0000000000000');
    render(ScheduleForm, { params: { id: 'sch_01jinvoices0000000000000' } });
    const select = (await screen.findByLabelText('Send results to')) as HTMLSelectElement;
    expect(select.selectedOptions[0].textContent).toBe('Here until you link a chat app');
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
