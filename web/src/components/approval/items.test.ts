import { fireEvent, render, within } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import type { InboxItem as ServerItem } from '../../lib/api/types.gen';
import { setCsrf } from '../../lib/api/client';
import { installApi } from '../../test/api';
import { decideItem, groupLines, itemQuestion, itemSource, loadItems } from './items';
import type { GroupChatItem } from './types';
import ReviewCard from './ReviewCard.svelte';

const at = '2026-10-14T09:00:00Z';
const inbox: ServerItem[] = [
  {
    id: 'sender:telegram:123456789',
    type: 'unknown_sender',
    question: '',
    for_you: true,
    created_at: at,
    sender: { channel: 'telegram', external_id: '123456789', display_name: 'Jordan Lee', username: '', first_text: 'Hi, is this the support bot?' },
  } as unknown as ServerItem,
  {
    id: 'sender:slack:U0123ABC',
    type: 'unknown_sender',
    question: '',
    for_you: true,
    created_at: at,
    sender: { channel: 'slack', external_id: 'U0123ABC', display_name: '', username: '', first_text: 'hello' },
  } as unknown as ServerItem,
  {
    id: 'mcp:github',
    type: 'mcp_review',
    question: '',
    for_you: true,
    created_at: at,
    mcp: { server: 'github', tools: [{ server: 'github', tool: 'create_issue', hash: 'h', definition: { description: 'Opens an issue' }, status: 'pending', side_effect: 'external_send', private: false }] },
  } as unknown as ServerItem,
];

async function items() {
  installApi({ override: (method, url) => (method === 'GET' && url === '/api/inbox' ? { items: inbox } : undefined) });
  return loadItems();
}

describe('inbox items', () => {
  it('names a new sender without showing their account id', async () => {
    const [jordan, nameless] = await items();
    expect(itemQuestion(jordan)).toBe('Someone new messaged Bothy on Telegram');
    expect(itemSource(jordan)).toBe('Telegram');
    expect(itemSource(nameless)).toBe('Slack');

    const { container } = render(ReviewCard, { item: jordan });
    const card = within(container);
    expect(container.textContent).toContain('Jordan Lee wrote: “Hi, is this the support bot?”');
    expect(container.textContent).not.toContain('123456789');
    await fireEvent.click(card.getByRole('button', { name: 'Details' }));
    expect(container.textContent).toContain('123456789');

    const other = render(ReviewCard, { item: nameless });
    expect(other.container.textContent).toContain('Someone wrote: “hello”');
    expect(other.container.textContent).not.toContain('U0123ABC');
  });

  it('calls a new server\'s actions new, not changes', async () => {
    const all = await items();
    const github = all.find((i) => i.id === 'mcp:github')!;
    expect(itemQuestion(github)).toBe("Review GitHub's new actions");
    expect(itemSource(github)).toBe('1 new action');
    const { container } = render(ReviewCard, { item: github });
    expect([...container.querySelectorAll('button[data-choice]')].map((b) => b.textContent?.trim())[0]).toBe('Approve actions');
    expect(container.textContent).not.toContain('Show what changed');
  });

  it('lets someone in or blocks them, and nothing else', async () => {
    const [jordan] = await items();
    const { container } = render(ReviewCard, { item: jordan });
    expect([...container.querySelectorAll('button[data-choice]')].map((b) => b.textContent?.trim())).toEqual(['Let in', 'Block']);
    expect(container.textContent).not.toMatch(/guest|Link/);
  });

  it('decides senders and group chats through their own endpoints', async () => {
    const calls = installApi({ override: (m) => (m === 'PATCH' ? {} : undefined) });
    setCsrf('mock-csrf');
    const sender = { id: 's', created_at: at, type: 'unknown_sender', app: 'telegram', external_id: '42', display_name: 'Jo', handle: '' } as const;
    const group = { id: 'g', created_at: at, type: 'group_chat', app: 'slack', chat: '#ops', space: 'spc_9', members: 0, external: false, mention_only: true } as const;
    await decideItem(sender, 'let_in');
    await decideItem(sender, 'block');
    await decideItem(group, 'turn_on', { tool_profile: 'standard' });
    await decideItem(group, 'turn_on');
    await decideItem(group, 'ignore');
    expect(calls.filter((c) => c.method === 'PATCH').map((c) => [c.url, JSON.parse(c.body)])).toEqual([
      ['/api/identities/telegram/42', { status: 'teammate' }],
      ['/api/identities/telegram/42', { status: 'blocked' }],
      ['/api/spaces/spc_9', { enabled: true, tool_profile: 'standard' }],
      ['/api/spaces/spc_9', { enabled: true, tool_profile: 'restricted' }],
      ['/api/spaces/spc_9', { enabled: false }],
    ]);
  });

  it('says what turning a group chat on means in each chat app', () => {
    const g = (x: Partial<GroupChatItem>): GroupChatItem => ({ id: 'g', created_at: at, type: 'group_chat', app: 'slack', chat: '#ops', space: 'spc_9', members: 0, external: false, mention_only: true, ...x });
    expect(groupLines(g({ added_by: 'Sam', members: 12 }))).toEqual(['Added by Sam.', '12 people are in this chat.']);
    expect(groupLines(g({ members: 1 }))).toEqual(['1 person is in this chat.']);
    expect(groupLines(g({ external: true }))).toEqual(["People from other companies are in this channel. They'll see Bothy's replies, though Bothy won't answer them."]);
    expect(groupLines(g({ app: 'telegram' }))).toEqual(["Anyone in this group can use Bothy once it's on, not only people you let in."]);
    expect(groupLines(g({ app: 'discord', team: 'Acme Community', members: 4812 }))).toEqual([
      'Acme Community has 4,812 members.',
      'Turning this on lets all 4,812 members of Acme Community message Bothy, here and in direct messages.',
    ]);
    // Without the server's name it is "the Discord server", never "this server" (Bothy's own host).
    expect(groupLines(g({ app: 'discord' }))).toEqual(['Turning this on lets everyone in the Discord server message Bothy, here and in direct messages.']);
    expect(groupLines(g({ app: 'discord', members: 40 }))).toEqual([
      'The Discord server has 40 members.',
      'Turning this on lets all 40 members of the Discord server message Bothy, here and in direct messages.',
    ]);
  });

  it('starts a new group chat on Answer questions, even one shared with other companies', () => {
    const { container } = render(ReviewCard, { item: { id: 'g', created_at: at, type: 'group_chat', app: 'slack', chat: '#ops', space: 'spc_9', members: 3, external: true, mention_only: false } });
    const radios = [...container.querySelectorAll<HTMLInputElement>('input[type="radio"]')];
    expect(radios.map((r) => r.checked)).toEqual([true, false]);
    expect(container.textContent).toContain('Bothy reads and answers. It can\'t change anything.');
    expect(container.textContent).toContain('Bothy can change files, run code and send things. An approver checks each step first.');
  });

  it('shows a changed description as two sentences, and any other change as the definition', async () => {
    const pin = (description: string, status: string, extra: object = {}) => ({ server: 'docs', tool: 'search_docs', hash: status, definition: { name: 'search_docs', description, ...extra }, status, side_effect: 'read', private: false });
    const review = (tools: object[]) =>
      ({ id: 'mcp:docs', type: 'mcp_review', question: '', for_you: true, created_at: at, mcp: { server: 'docs', tools } }) as unknown as ServerItem;
    installApi({
      override: (m, url) =>
        m === 'GET' && url === '/api/inbox'
          ? { items: [review([pin('Search the handbook and return matching sections.', 'pending'), pin('Search the handbook.', 'superseded')])] }
          : undefined,
    });
    const [only] = await loadItems();
    expect(itemSource(only)).toBe('1 action changed');
    const prose = only.type === 'mcp_review' ? only.tools[0] : undefined;
    expect(prose).toMatchObject({ prose: true, diff: '-Search the handbook.\n+Search the handbook and return matching sections.' });
    installApi({
      override: (m, url) =>
        m === 'GET' && url === '/api/inbox' ? { items: [review([pin('Search.', 'pending', { inputSchema: { q: 1 } }), pin('Search.', 'superseded')])] } : undefined,
    });
    const [schema] = await loadItems();
    expect(schema.type === 'mcp_review' && schema.tools[0].prose).toBe(false);
  });

  // The review in Approvals is the one people reach first; a server's readOnlyHint showed only on its own page.
  it("says when a server calls an action read-only, leaving the class as it was", async () => {
    const pin = (tool: string, definition: object) => ({ server: 'acme-helpdesk', tool, hash: tool, definition: { name: tool, ...definition }, status: 'pending', side_effect: 'external_send', private: false });
    installApi({
      override: (m, url) =>
        m === 'GET' && url === '/api/inbox'
          ? {
              items: [
                {
                  id: 'mcp:acme-helpdesk',
                  type: 'mcp_review',
                  question: '',
                  for_you: true,
                  created_at: at,
                  mcp: { server: 'acme-helpdesk', tools: [pin('echo', { annotations: { readOnlyHint: true } }), pin('create_ticket', { annotations: { readOnlyHint: false } })] },
                },
              ],
            }
          : undefined,
    });
    const [review] = await loadItems();
    const { container } = render(ReviewCard, { item: review });
    const rows = [...container.querySelectorAll('li')];
    expect(rows[0].textContent).toContain('Acme helpdesk says this only reads information.');
    expect(rows[1].textContent).not.toContain('only reads information.');
    expect((within(rows[0]).getByLabelText('What this does') as HTMLSelectElement).value).toBe('external_send');
  });

  it('spells a service the way it spells itself', async () => {
    const review = (await items())[2];
    expect(itemQuestion(review)).toBe("Review GitHub's new actions");
    const { container } = render(ReviewCard, { item: review });
    expect(within(container).getByRole('button', { name: 'Turn off GitHub' })).toBeTruthy();
  });
});
