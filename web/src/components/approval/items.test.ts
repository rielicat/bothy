import { fireEvent, render, within } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import type { InboxItem as ServerItem } from '../../lib/api/types.gen';
import { installApi } from '../../test/api';
import { itemQuestion, itemSource, loadItems } from './items';
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

  it('spells a service the way it spells itself', async () => {
    const review = (await items())[2];
    expect(itemQuestion(review)).toBe('Review GitHub actions');
    const { container } = render(ReviewCard, { item: review });
    expect(within(container).getByRole('button', { name: 'Turn off GitHub' })).toBeTruthy();
  });
});
