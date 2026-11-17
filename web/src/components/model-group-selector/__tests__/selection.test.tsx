import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'

import { ModelGroupSelector } from '../../model-group-selector'

describe('model capability selection', () => {
  it('groups models by capability and allows selecting an API-only model', async () => {
    const onModelChange = vi.fn()
    const user = userEvent.setup()
    render(
      <ModelGroupSelector
        selectedModel='text-model'
        selectedGroup='default'
        models={[
          {
            value: 'text-model',
            label: 'text-model',
            category: 'Text',
            chatSupported: true,
          },
          {
            value: 'video-model',
            label: 'video-model',
            category: 'Video',
            chatSupported: false,
          },
        ]}
        groups={[{ value: 'default', label: 'Default' }]}
        onModelChange={onModelChange}
        onGroupChange={vi.fn()}
      />
    )
    await user.click(screen.getByRole('combobox'))
    expect(await screen.findByText('Video models')).toBeVisible()
    const video = screen.getByRole('option', { name: /video-model/ })
    expect(screen.getByText('API only - no chat')).toBeVisible()
    await user.click(video)
    expect(onModelChange).toHaveBeenCalledWith('video-model')
  })

  it('shows all five categories in the requested order and retains API labels when searching', async () => {
    const user = userEvent.setup()
    render(
      <ModelGroupSelector
        selectedModel='text-model'
        selectedGroup='default'
        models={[
          {
            value: 'embedding-model',
            label: 'embedding-model',
            category: 'Embedding',
            chatSupported: false,
          },
          {
            value: 'audio-model',
            label: 'audio-model',
            category: 'Audio',
            chatSupported: false,
          },
          {
            value: 'image-model',
            label: 'image-model',
            category: 'Image',
            chatSupported: false,
          },
          {
            value: 'video-model',
            label: 'video-model',
            category: 'Video',
            chatSupported: false,
          },
          {
            value: 'text-model',
            label: 'text-model',
            category: 'Text',
            chatSupported: true,
          },
        ]}
        groups={[{ value: 'default', label: 'Default' }]}
        onModelChange={vi.fn()}
        onGroupChange={vi.fn()}
      />
    )
    await user.click(screen.getByRole('combobox'))
    expect(
      screen
        .getAllByRole('option')
        .map((option) => option.getAttribute('data-value'))
    ).toEqual([
      'text-model',
      'video-model',
      'image-model',
      'audio-model',
      'embedding-model',
    ])
    for (const heading of [
      'Text models',
      'Video models',
      'Image models',
      'Audio models',
      'Embedding models',
    ]) {
      expect(screen.getByText(heading)).toBeVisible()
    }
    await user.type(screen.getByPlaceholderText('Search models...'), 'video')
    expect(screen.getAllByRole('option')).toHaveLength(1)
    expect(screen.getByRole('option', { name: /video-model/ })).toBeEnabled()
    expect(screen.getByText('API only - no chat')).toBeVisible()
  })

  it('allows selecting API models with the keyboard', async () => {
    const onModelChange = vi.fn()
    const user = userEvent.setup()
    render(
      <ModelGroupSelector
        selectedModel='first'
        selectedGroup='default'
        models={[
          {
            value: 'first',
            label: 'first',
            category: 'Text',
            chatSupported: true,
          },
          {
            value: 'api-only',
            label: 'api-only',
            category: 'Text',
            chatSupported: false,
          },
          {
            value: 'second',
            label: 'second',
            category: 'Text',
            chatSupported: true,
          },
        ]}
        groups={[{ value: 'default', label: 'Default' }]}
        onModelChange={onModelChange}
        onGroupChange={vi.fn()}
      />
    )
    await user.click(screen.getByRole('combobox'))
    await user.click(screen.getByPlaceholderText('Search models...'))
    await user.keyboard('{Home}{ArrowDown}{Enter}')
    expect(onModelChange).toHaveBeenCalledWith('api-only')
  })
})
