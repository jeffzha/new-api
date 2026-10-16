import { render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'

import { PlaygroundInput } from '../playground-input'

const config = {
  model: 'video-model',
  group: 'default',
  temperature: 1,
  top_p: 1,
  max_tokens: 1024,
  frequency_penalty: 0,
  presence_penalty: 0,
  seed: null,
  stream: true,
}

const parameterEnabled = {
  temperature: true,
  top_p: true,
  max_tokens: true,
  frequency_penalty: false,
  presence_penalty: false,
  seed: false,
}

describe('PlaygroundInput model capabilities', () => {
  it('shows an API-only notice and disables composing and sending', () => {
    const onSubmit = vi.fn()
    render(
      <PlaygroundInput
        canSend={false}
        config={config}
        groups={[{ value: 'default', label: 'Default', ratio: 1 }]}
        groupValue='default'
        models={[
          {
            value: 'video-model',
            label: 'video-model',
            category: 'Video',
            chatSupported: false,
          },
        ]}
        modelValue='video-model'
        onConfigChange={vi.fn()}
        onGroupChange={vi.fn()}
        onModelChange={vi.fn()}
        onParameterEnabledChange={vi.fn()}
        onSubmit={onSubmit}
        parameterEnabled={parameterEnabled}
      />
    )

    expect(screen.getByRole('status')).toHaveTextContent(
      'This model does not support chat in Playground. Call it through the API.'
    )
    expect(
      screen.getByPlaceholderText(
        'This model is available through API calls only'
      )
    ).toBeDisabled()
    for (const button of screen.getAllByRole('button', { name: /Send/ })) {
      expect(button).toBeDisabled()
    }
    expect(onSubmit).not.toHaveBeenCalled()
  })
})
