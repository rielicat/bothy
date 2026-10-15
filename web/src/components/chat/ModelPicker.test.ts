import { fireEvent, render, screen, within } from '@testing-library/svelte';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { ModelInfo } from '../../lib/api/types.gen';
import ModelPicker from './ModelPicker.svelte';
import { models } from './models.svelte';

const info = (provider: string, model: string, display: string, extra: Partial<ModelInfo> = {}) =>
  ({ ref: { provider, model }, display, display_name: display, ...extra }) as ModelInfo;

describe('ModelPicker', () => {
  afterEach(() => {
    models.list = [];
    models.main = models.fast = '';
  });

  it("keeps a router's long list to suggestions and finds the rest by search", async () => {
    models.list = [
      info('openrouter', 'anthropic/claude-sonnet-5.5', 'Claude Sonnet 5.5', { recommended: true }),
      info('openrouter', 'anthropic/claude-haiku-5.5', 'Claude Haiku 5.5', { quick_tasks_default: true }),
      info('openai', 'gpt-6.1-sol', 'GPT-6.1 Sol', { recommended: true }),
      ...Array.from({ length: 12 }, (_, i) => info('openrouter', `vendor/model-${i}`, `Model ${i}`)),
    ];
    models.main = 'openrouter/anthropic/claude-sonnet-5.5';
    models.fast = 'openrouter/anthropic/claude-haiku-5.5';
    const onchange = vi.fn();
    render(ModelPicker, { value: '', onchange });
    await fireEvent.click(screen.getByRole('button', { name: 'Model: Claude Sonnet 5.5' }));
    const menu = await screen.findByRole('menu');
    expect(within(menu).getAllByRole('menuitemradio').map((i) => i.textContent?.trim())).toEqual([
      'Main model · Claude Sonnet 5.5',
      'Quick tasks model · Claude Haiku 5.5',
      'GPT-6.1 Sol',
    ]);
    expect(within(menu).queryByText('Model 3')).toBeNull();
    await fireEvent.click(within(menu).getByText('More models…'));
    const dialog = await screen.findByRole('dialog', { name: 'Model for this chat' });
    await fireEvent.input(within(dialog).getByLabelText('Search models'), { target: { value: 'model 3' } });
    await fireEvent.click(within(dialog).getByRole('option', { name: 'Model 3' }));
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }));
    expect(onchange).toHaveBeenCalledWith('openrouter/vendor/model-3');
  });
});
