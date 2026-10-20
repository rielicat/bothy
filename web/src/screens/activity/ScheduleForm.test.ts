import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { beforeEach, describe, expect, it } from 'vitest';
import { router } from '../../lib/router.svelte';
import { session } from '../../lib/stores/session.svelte';
import { toaster } from '../../components/ui';
import { installApi, type Call } from '../../test/api';
import ScheduleForm from './ScheduleForm.svelte';

let calls: Call[];

beforeEach(async () => {
  calls = installApi();
  session.ready = false;
  await session.init();
});

const posted = () => calls.filter((c) => c.method === 'POST' && c.url === '/api/schedules');

describe('A schedule the server refuses', () => {
  it('says why, since trying again would not help', async () => {
    const why = 'Mon 19 Oct 2026 09:00 has already passed. Choose a later time.';
    calls = installApi({ override: (m, url) => (m === 'POST' && url === '/api/schedules' ? new Response(JSON.stringify({ error: { code: 'bad_request', message: why } }), { status: 400 }) : undefined) });
    await session.init();
    router.navigate('/activity/schedules/new');
    render(ScheduleForm, { params: { id: 'new' } });
    await fireEvent.input(await screen.findByLabelText('What should Bothy do?'), { target: { value: 'Say hello' } });
    await fireEvent.click(screen.getByRole('button', { name: 'Create schedule' }));
    expect(await screen.findByText(why)).toBeTruthy();
    expect(screen.queryByText("Couldn't save. Try again.")).toBeNull();
  });
});

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

  it('sends results to the chat list, the only place an account has, and says so in the preview', async () => {
    // A fact, not a field with nothing to choose.
    expect(screen.queryByText('Send results to')).toBeNull();
    await waitFor(() => expect(screen.getByText(/^Runs /).textContent).toMatch(/^Runs weekdays at .+ London time\. Each result gets its own chat in your list\. Next: /));
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
    expect(line()).toMatch(/^Runs Mondays at .+ London time\. Each result gets its own chat in your list\.$/);
    await waitFor(() => expect(line()).toMatch(/Next: /));
  });

  it('saves a one-off run as wall-clock time in the schedule time zone', async () => {
    await fireEvent.change(screen.getByLabelText('Repeat'), { target: { value: 'once' } });
    await fireEvent.input(screen.getByLabelText('Date'), { target: { value: '2026-11-02' } });
    expect(screen.getByText(/^Runs /).textContent).toMatch(/^Runs once on .+ London time\. The result gets its own chat in your list\.$/);
    await fireEvent.input(screen.getByLabelText('What should Bothy do?'), { target: { value: 'Remind the team about the launch.' } });
    await fireEvent.click(screen.getByRole('button', { name: 'Create schedule' }));
    await waitFor(() => expect(posted()).toHaveLength(1));
    expect(JSON.parse(posted()[0].body).schedule).toMatchObject({ kind: 'once', spec: '2026-11-02T09:00', tz: 'Europe/London' });
  });
});

describe('A schedule from an example (docs/design/ui.md §10.3)', () => {
  const news = 'Search the web for news from the last day about [your topic] and list the 5 most useful items, each with a one-line summary and a link.';
  const examples = {
    examples: [
      { id: 'pr-summary', name: 'Monday pull request summary', kind: 'cron', spec: '0 9 * * 1', slot: 'main', needs: 'github', prompt: 'Summarise the pull requests in acme/web from the last 7 days.', ready: true },
      { id: 'news', name: 'Morning news on a topic', kind: 'cron', spec: '0 8 * * 1-5', slot: 'fast', needs: 'web_search', prompt: news, ready: true },
      { id: 'build-check', name: 'Morning build check', kind: 'cron', spec: '30 8 * * 1-5', slot: 'fast', needs: 'github', prompt: '', ready: false },
    ],
    checkin: { empty: false, lines: [] },
  };

  async function open(example: string) {
    calls = installApi({ override: (m, url) => (url === '/api/schedules/examples' ? examples : undefined) });
    router.navigate(`/activity/schedules/new?example=${example}`);
    render(ScheduleForm, { params: { id: 'new' } });
    return (await screen.findByLabelText('What should Bothy do?')) as HTMLTextAreaElement;
  }

  it("starts blank for an example that can't run yet", async () => {
    expect((await open('build-check')).value).toBe('');
    expect((screen.getByLabelText('Name') as HTMLInputElement).value).toBe('');
  });

  it('prefills what to do, when, the name and the model in the person\'s time zone, and saves it as a new schedule', async () => {
    expect((await open('pr-summary')).value).toBe('Summarise the pull requests in acme/web from the last 7 days.');
    expect((screen.getByLabelText('Name') as HTMLInputElement).value).toBe('Monday pull request summary');
    expect((screen.getByLabelText('Repeat') as HTMLSelectElement).value).toBe('weekly');
    expect((screen.getByLabelText('Day of the week') as HTMLSelectElement).value).toBe('1');
    expect((screen.getByLabelText('Time') as HTMLSelectElement).value).toBe('09:00');
    await fireEvent.click(screen.getByRole('button', { name: 'More options' }));
    expect((screen.getByLabelText('Model') as HTMLSelectElement).value).toBe('main');
    expect((screen.getByLabelText('If Bothy was off') as HTMLSelectElement).value).toBe('once');
    // A name from the example stays when the task changes.
    await fireEvent.input(screen.getByLabelText('What should Bothy do?'), { target: { value: 'Summarise the pull requests in acme/api from the last 7 days.' } });
    expect((screen.getByLabelText('Name') as HTMLInputElement).value).toBe('Monday pull request summary');
    await fireEvent.click(screen.getByRole('button', { name: 'Create schedule' }));
    await waitFor(() => expect(posted()).toHaveLength(1));
    expect(JSON.parse(posted()[0].body).schedule).toMatchObject({ id: '', kind: 'cron', spec: '0 9 * * 1', tz: 'Europe/London', slot: 'main', missed_run: 'once', name: 'Monday pull request summary' });
  });

  it('opens a blank form for an example it no longer offers', async () => {
    expect((await open('release-notes')).value).toBe('');
    expect((screen.getByLabelText('Name') as HTMLInputElement).value).toBe('');
  });

  it("won't save an example's placeholder, and selects it so typing replaces it", async () => {
    const box = await open('news');
    await fireEvent.click(screen.getByRole('button', { name: 'Create schedule' }));
    expect(await screen.findByText('Replace [your topic] first.')).toBeTruthy();
    expect(box.getAttribute('aria-invalid')).toBe('true');
    await waitFor(() => expect(document.activeElement).toBe(box));
    expect(box.value.slice(box.selectionStart, box.selectionEnd)).toBe('[your topic]');
    expect(posted()).toHaveLength(0);
    await fireEvent.input(box, { target: { value: news.replace('[your topic]', 'electric bikes') } });
    expect(screen.queryByText('Replace [your topic] first.')).toBeNull();
    await fireEvent.click(screen.getByRole('button', { name: 'Create schedule' }));
    await waitFor(() => expect(posted()).toHaveLength(1));
  });

  it('saves brackets someone typed: only an example leaves placeholders', async () => {
    const box = await open('nope');
    await fireEvent.input(box, { target: { value: 'Post the [DRAFT] release notes.' } });
    await fireEvent.click(screen.getByRole('button', { name: 'Create schedule' }));
    await waitFor(() => expect(posted()).toHaveLength(1));
  });
});

describe('Edit schedule', () => {
  // Run now used to be only in the list row's menu.
  it("runs the schedule from its own page's menu", async () => {
    toaster.dismiss();
    router.navigate('/activity/schedules/sch_01jstandup00000000000000');
    render(ScheduleForm, { params: { id: 'sch_01jstandup00000000000000' } });
    await fireEvent.click(await screen.findByRole('button', { name: 'More for this schedule' }));
    await fireEvent.click(await screen.findByRole('menuitem', { name: 'Run now' }));
    await waitFor(() => expect(calls.some((c) => c.method === 'POST' && c.url === '/api/schedules/sch_01jstandup00000000000000/run')).toBe(true));
    await waitFor(() => expect(toaster.current?.text).toBe('Schedule started.'));
    expect(toaster.current?.action?.label).toBe('Open chat');
  });

  it('opens a rule that fits no choice as Custom and lists recent runs', async () => {
    router.navigate('/activity/schedules/sch_01jinvoices0000000000000');
    render(ScheduleForm, { params: { id: 'sch_01jinvoices0000000000000' } });
    expect(((await screen.findByLabelText('Repeat rule')) as HTMLInputElement).value).toBe('0 9 1,15 * *');
    expect(screen.getByRole('heading', { level: 1 }).textContent).toBe('Invoice reminder');
    expect(screen.getByRole('button', { name: 'Save schedule' })).toBeTruthy();
  });

  it("names each recent run by when it ran and how it went, not by the page's own title", async () => {
    router.navigate('/activity/schedules/sch_01jstandup00000000000000');
    render(ScheduleForm, { params: { id: 'sch_01jstandup00000000000000' } });
    const recent = (await screen.findByRole('heading', { name: 'Recent runs' })).closest('section')!;
    const rows = await waitFor(() => {
      const r = [...recent.querySelectorAll('li')];
      expect(r.length).toBeGreaterThan(0);
      return r;
    });
    for (const r of rows) {
      expect(r.textContent).toMatch(/^\s*(Today|Yesterday|\w+day \d+ \w+) at /);
      expect(r.textContent).not.toContain('Daily standup summary');
      expect(r.textContent).not.toMatch(/\bSchedule\b/);
    }
    expect(rows[0].textContent).toMatch(/Posted to your chats|Failed/);
  });

  it('keeps the check-in on its own form', async () => {
    router.navigate('/activity/schedules/checkin');
    render(ScheduleForm, { params: { id: 'checkin' } });
    expect(await screen.findByLabelText('What to check')).toBeTruthy();
    expect(screen.getByText('Bothy looks through this list regularly and messages you only when something needs attention.')).toBeTruthy();
    expect(await screen.findByText(/a day with Claude Haiku 4\.5\.$/)).toBeTruthy();
  });
});
