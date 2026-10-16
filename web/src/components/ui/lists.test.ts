import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { createRawSnippet } from 'svelte';
import { describe, expect, it, vi } from 'vitest';
import Combobox, { filterOptions } from './Combobox.svelte';
import { findTypeahead } from './layer';
import type { MenuItem } from './Menu.svelte';
import OptionList from './OptionList.svelte';
import ListboxHarness from './test/ListboxHarness.svelte';
import MenuHarness from './test/MenuHarness.svelte';

describe('Menu', () => {
  const setup = () => {
    const picked: string[] = [];
    const items: MenuItem[] = [
      { label: 'Rename', onselect: () => picked.push('Rename') },
      { label: 'Pin', onselect: () => picked.push('Pin') },
      { label: 'Share', disabled: true, reason: 'Only the person who started the chat can share it.', onselect: () => picked.push('Share') },
      'separator',
      { label: 'Delete chat…', danger: true, onselect: () => picked.push('Delete') },
    ];
    render(MenuHarness, { props: { items } });
    return { picked, trigger: screen.getByRole('button', { name: 'Chat options' }) };
  };
  const focused = () => document.activeElement?.textContent?.trim();

  it('opens from the keyboard on the first item and moves with the arrows', async () => {
    const { trigger } = setup();
    expect(trigger.getAttribute('aria-haspopup')).toBe('menu');
    expect(trigger.getAttribute('aria-expanded')).toBe('false');
    await fireEvent.keyDown(trigger, { key: 'ArrowDown' });
    await waitFor(() => expect(focused()).toBe('Rename'));
    expect(trigger.getAttribute('aria-expanded')).toBe('true');
    expect(screen.getByRole('menu').id).toBe(trigger.getAttribute('aria-controls'));
    await fireEvent.keyDown(document.activeElement!, { key: 'ArrowDown' });
    expect(focused()).toBe('Pin');
    await fireEvent.keyDown(document.activeElement!, { key: 'End' });
    expect(focused()).toBe('Delete chat…');
    await fireEvent.keyDown(document.activeElement!, { key: 'ArrowDown' });
    expect(focused()).toBe('Rename');
    await fireEvent.keyDown(document.activeElement!, { key: 'ArrowUp' });
    expect(focused()).toBe('Delete chat…');
  });

  it('opens on the last item with ArrowUp', async () => {
    const { trigger } = setup();
    await fireEvent.keyDown(trigger, { key: 'ArrowUp' });
    await waitFor(() => expect(focused()).toBe('Delete chat…'));
  });

  it('jumps by typing the start of a label', async () => {
    const { trigger } = setup();
    await fireEvent.keyDown(trigger, { key: 'ArrowDown' });
    await waitFor(() => expect(focused()).toBe('Rename'));
    const now = vi.spyOn(Date, 'now').mockReturnValue(10_000);
    await fireEvent.keyDown(document.activeElement!, { key: 'd' });
    expect(focused()).toBe('Delete chat…');
    now.mockReturnValue(11_000);
    await fireEvent.keyDown(document.activeElement!, { key: 'p' });
    expect(focused()).toBe('Pin');
    // The same letter again, quickly, moves to the next match.
    await fireEvent.keyDown(document.activeElement!, { key: 'r' });
    expect(focused()).toBe('Pin');
    now.mockReturnValue(12_000);
    await fireEvent.keyDown(document.activeElement!, { key: 'r' });
    expect(focused()).toBe('Rename');
    await fireEvent.keyDown(document.activeElement!, { key: 'r' });
    expect(focused()).toBe('Rename');
  });

  it('closes on Esc and returns focus to the trigger', async () => {
    const { trigger, picked } = setup();
    await fireEvent.keyDown(trigger, { key: 'ArrowDown' });
    await waitFor(() => expect(screen.queryByRole('menu')).not.toBeNull());
    await fireEvent.keyDown(document.activeElement!, { key: 'Escape' });
    expect(screen.queryByRole('menu')).toBeNull();
    expect(document.activeElement).toBe(trigger);
    expect(picked).toEqual([]);
  });

  it('runs an item, closes, and never runs a disabled one', async () => {
    const { trigger, picked } = setup();
    await fireEvent.click(trigger);
    await waitFor(() => expect(screen.queryByRole('menu')).not.toBeNull());
    const share = screen.getByRole('menuitem', { name: /Share/ });
    expect(share.getAttribute('aria-disabled')).toBe('true');
    expect(share.textContent).toContain('Only the person who started the chat can share it.');
    await fireEvent.click(share);
    expect(picked).toEqual([]);
    await fireEvent.click(screen.getByRole('menuitem', { name: 'Delete chat…' }));
    expect(picked).toEqual(['Delete']);
    expect(screen.queryByRole('menu')).toBeNull();
    expect(document.activeElement).toBe(trigger);
  });

  it('closes on a press outside', async () => {
    const { trigger } = setup();
    await fireEvent.click(trigger);
    await waitFor(() => expect(screen.queryByRole('menu')).not.toBeNull());
    await fireEvent.pointerDown(screen.getByRole('button', { name: 'Elsewhere' }));
    expect(screen.queryByRole('menu')).toBeNull();
  });

  it('marks the current choice of a group with menuitemradio', async () => {
    render(MenuHarness, {
      props: {
        items: [
          { label: 'Light', checked: false, group: 'Theme', onselect: () => {} },
          { label: 'Dark', checked: true, group: 'Theme', onselect: () => {} },
        ],
      },
    });
    await fireEvent.click(screen.getByRole('button', { name: 'Chat options' }));
    await waitFor(() => expect(screen.getAllByRole('menuitemradio')).toHaveLength(2));
    expect(screen.getByRole('menuitemradio', { name: 'Dark' }).getAttribute('aria-checked')).toBe('true');
    expect(screen.getByRole('group', { name: 'Theme' })).toBeTruthy();
  });

  it('marks a setting that turns on or off with menuitemcheckbox', async () => {
    render(MenuHarness, { props: { items: [{ label: 'Can approve', checked: true, toggle: true, onselect: () => {} }, { label: 'Block…', onselect: () => {} }] } });
    await fireEvent.click(screen.getByRole('button', { name: 'Chat options' }));
    const item = await screen.findByRole('menuitemcheckbox', { name: 'Can approve' });
    expect(item.getAttribute('aria-checked')).toBe('true');
    expect(screen.queryByRole('menuitemradio')).toBeNull();
  });

  it('typeahead cycles through matches and wraps', () => {
    const labels = ['Rename', 'Pin', 'Pause', 'Delete'];
    expect(findTypeahead(labels, 'p', 0)).toBe(1);
    expect(findTypeahead(labels, 'p', 1)).toBe(2);
    expect(findTypeahead(labels, 'p', 2)).toBe(1);
    expect(findTypeahead(labels, 'pa', 1)).toBe(2);
    expect(findTypeahead(labels, 'x', 0)).toBe(-1);
  });
});

describe('Listbox', () => {
  const options = [
    { value: 'sonnet', label: 'Claude Sonnet 4.5', group: 'Anthropic' },
    { value: 'haiku', label: 'Claude Haiku 4.5', group: 'Anthropic' },
    { value: 'qwen', label: 'Qwen3 14B', group: 'Ollama' },
  ];

  it('points aria-activedescendant at the selected option, moves it and chooses with Enter', async () => {
    const onchange = vi.fn();
    render(ListboxHarness, { props: { options, value: 'haiku', onchange } });
    const trigger = screen.getByRole('button', { name: 'Claude Haiku 4.5' });
    expect(trigger.getAttribute('aria-haspopup')).toBe('listbox');
    await fireEvent.click(trigger);
    const list = await screen.findByRole('listbox', { name: 'Model' });
    await waitFor(() => expect(document.activeElement).toBe(list));
    const active = () => document.getElementById(list.getAttribute('aria-activedescendant')!);
    expect(active()?.textContent).toContain('Claude Haiku 4.5');
    expect(active()?.getAttribute('aria-selected')).toBe('true');
    await fireEvent.keyDown(list, { key: 'ArrowDown' });
    expect(active()?.textContent).toContain('Qwen3 14B');
    await fireEvent.keyDown(list, { key: 'Home' });
    expect(active()?.textContent).toContain('Claude Sonnet 4.5');
    await fireEvent.keyDown(list, { key: 'q' });
    expect(active()?.textContent).toContain('Qwen3 14B');
    await fireEvent.keyDown(list, { key: 'Enter' });
    expect(onchange).toHaveBeenCalledWith('qwen');
    expect(screen.queryByRole('listbox')).toBeNull();
    expect(document.activeElement?.textContent).toBe('Qwen3 14B');
  });

  it('closes on Esc without changing the value', async () => {
    const onchange = vi.fn();
    render(ListboxHarness, { props: { options, value: 'sonnet', onchange } });
    const trigger = screen.getByRole('button', { name: 'Claude Sonnet 4.5' });
    await fireEvent.keyDown(trigger, { key: 'ArrowDown' });
    const list = await screen.findByRole('listbox');
    await fireEvent.keyDown(list, { key: 'ArrowDown' });
    await fireEvent.keyDown(list, { key: 'Escape' });
    expect(screen.queryByRole('listbox')).toBeNull();
    expect(onchange).not.toHaveBeenCalled();
    expect(document.activeElement).toBe(trigger);
  });

  it('groups options under their provider', async () => {
    render(ListboxHarness, { props: { options, value: 'sonnet' } });
    await fireEvent.click(screen.getByRole('button', { name: 'Claude Sonnet 4.5' }));
    await screen.findByRole('listbox');
    expect(screen.getByRole('group', { name: 'Ollama' }).textContent).toContain('Qwen3 14B');
  });
});

describe('Combobox', () => {
  const zones = [
    { value: 'Europe/London', label: 'London' },
    { value: 'America/Sao_Paulo', label: 'São Paulo' },
    { value: 'Asia/Tokyo', label: 'Tokyo' },
  ];

  it('filters while typing and tracks the active option', async () => {
    const onchange = vi.fn();
    render(Combobox, { props: { label: 'Time zone', options: zones, value: 'Europe/London', onchange } });
    const input = screen.getByRole('combobox', { name: 'Time zone' }) as HTMLInputElement;
    expect(input.value).toBe('London');
    expect(input.getAttribute('aria-expanded')).toBe('false');
    await fireEvent.input(input, { target: { value: 'sao' } });
    expect(input.getAttribute('aria-expanded')).toBe('true');
    const list = screen.getByRole('listbox');
    expect(input.getAttribute('aria-controls')).toBe(list.id);
    expect(screen.getAllByRole('option').map((o) => o.textContent?.trim())).toEqual(['São Paulo']);
    expect(document.getElementById(input.getAttribute('aria-activedescendant')!)?.textContent).toContain('São Paulo');
    await fireEvent.keyDown(input, { key: 'Enter' });
    expect(onchange).toHaveBeenCalledWith('America/Sao_Paulo');
    expect(input.value).toBe('São Paulo');
    expect(screen.queryByRole('listbox')).toBeNull();
  });

  it('says when nothing matches', async () => {
    render(Combobox, { props: { label: 'Time zone', options: zones } });
    const input = screen.getByRole('combobox', { name: 'Time zone' });
    await fireEvent.input(input, { target: { value: 'zz' } });
    expect(screen.getByText('No matches')).toBeTruthy();
    expect(screen.queryAllByRole('option')).toHaveLength(0);
    expect(input.getAttribute('aria-activedescendant')).toBeNull();
  });

  it('moves with the arrows and closes on Esc', async () => {
    render(Combobox, { props: { label: 'Time zone', options: zones, value: 'Europe/London' } });
    const input = screen.getByRole('combobox', { name: 'Time zone' }) as HTMLInputElement;
    await fireEvent.keyDown(input, { key: 'ArrowDown' });
    const active = () => document.getElementById(input.getAttribute('aria-activedescendant')!)?.textContent?.trim();
    expect(active()).toBe('London');
    await fireEvent.keyDown(input, { key: 'ArrowDown' });
    expect(active()).toBe('São Paulo');
    await fireEvent.keyDown(input, { key: 'Escape' });
    expect(screen.queryByRole('listbox')).toBeNull();
    expect(input.value).toBe('London');
  });

  it('chooses several and lists them under the field', async () => {
    const onchange = vi.fn();
    const team = [
      { value: 'alex', label: 'Alex Kim' },
      { value: 'sam', label: 'Sam Patel' },
      { value: 'priya', label: 'Priya Shah' },
    ];
    render(Combobox, { props: { label: 'Approvers', options: team, multiple: true, value: ['alex'], onchange } });
    const input = screen.getByRole('combobox', { name: 'Approvers' });
    expect(screen.getByText('Alex Kim')).toBeTruthy();
    await fireEvent.click(input);
    expect(screen.getByRole('listbox').getAttribute('aria-multiselectable')).toBe('true');
    await fireEvent.click(screen.getByRole('option', { name: 'Priya Shah' }));
    expect(onchange).toHaveBeenLastCalledWith(['alex', 'priya']);
    expect(screen.getByRole('listbox')).toBeTruthy();
    expect(screen.getByText('Alex Kim, Priya Shah')).toBeTruthy();
    expect(screen.getByRole('option', { name: 'Alex Kim' }).getAttribute('aria-selected')).toBe('true');
    await fireEvent.click(screen.getByRole('option', { name: 'Alex Kim' }));
    expect(onchange).toHaveBeenLastCalledWith(['priya']);
    await fireEvent.keyDown(input, { key: 'Backspace' });
    expect(onchange).toHaveBeenLastCalledWith([]);
  });

  it('matches without case or accents', () => {
    expect(filterOptions(zones, 'SAO').map((o) => o.label)).toEqual(['São Paulo']);
    expect(filterOptions(zones, '  ')).toHaveLength(3);
  });
});

describe('OptionList', () => {
  const options = [
    { value: 'careful', title: 'Careful', description: 'Asks before changing files.' },
    { value: 'balanced', title: 'Balanced' },
    { value: 'hands-off', title: 'Hands-off', disabled: true, reason: 'Turn on running code first.' },
  ];
  const expanded = createRawSnippet((value: () => string) => ({ render: () => `<p>Extra controls for ${value()}</p>` }));

  it('moves the choice with the arrow keys, skipping disabled options', async () => {
    const onchange = vi.fn();
    render(OptionList, { props: { name: 'preset', options, value: 'careful', onchange, label: 'When should Bothy ask?' } });
    const radios = screen.getAllByRole('radio') as HTMLInputElement[];
    expect(screen.getByRole('radiogroup', { name: 'When should Bothy ask?' })).toBeTruthy();
    expect(radios[0].checked).toBe(true);
    await fireEvent.keyDown(radios[0], { key: 'ArrowDown' });
    expect(onchange).toHaveBeenLastCalledWith('balanced');
    expect(document.activeElement).toBe(radios[1]);
    expect(radios[1].checked).toBe(true);
    await fireEvent.keyDown(radios[1], { key: 'ArrowDown' });
    expect(onchange).toHaveBeenLastCalledWith('careful');
    expect(document.activeElement).toBe(radios[0]);
    await fireEvent.keyDown(radios[0], { key: 'ArrowUp' });
    expect(onchange).toHaveBeenLastCalledWith('balanced');
  });

  it('shows the disabled reason and the expanded slot only for the chosen option', async () => {
    render(OptionList, { props: { name: 'preset', options, value: 'careful', expanded } });
    expect(screen.getByText('Extra controls for careful')).toBeTruthy();
    expect(screen.getByText('Turn on running code first.')).toBeTruthy();
    const hands = screen.getByRole('radio', { name: 'Hands-off' }) as HTMLInputElement;
    expect(hands.disabled).toBe(true);
    await fireEvent.click(screen.getByRole('radio', { name: 'Balanced' }));
    expect(screen.queryByText('Extra controls for careful')).toBeNull();
    expect(screen.getByText('Extra controls for balanced')).toBeTruthy();
  });
});
