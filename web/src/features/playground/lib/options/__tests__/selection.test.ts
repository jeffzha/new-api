import { describe, expect, it } from 'vitest'

import type { ModelOption } from '../../../types'
import {
  getModelFallback,
  shouldClearModelForGroup,
} from '../playground-option-utils'

const models: ModelOption[] = [
  { label: 'text-model', value: 'text-model', chatSupported: true },
  {
    label: 'video-model',
    value: 'video-model',
    category: 'Video',
    chatSupported: false,
  },
]

describe('playground model capability handling', () => {
  it('keeps an API-only model selected so the playground can explain its limitation', () => {
    expect(getModelFallback(models, 'video-model')).toBeNull()
    expect(shouldClearModelForGroup(models, 'video-model')).toBe(false)
  })

  it('keeps a selected chat-capable model', () => {
    expect(getModelFallback(models, 'text-model')).toBeNull()
    expect(shouldClearModelForGroup(models, 'text-model')).toBe(false)
  })

  it('keeps a valid selection when the group has only API models', () => {
    expect(getModelFallback([models[1]], 'video-model')).toBeNull()
    expect(shouldClearModelForGroup([models[1]], 'video-model')).toBe(false)
  })

  it('prefers a chat model only when the current selection is missing', () => {
    expect(getModelFallback(models, 'missing-model')).toBe('text-model')
    expect(shouldClearModelForGroup(models, 'missing-model')).toBe(true)
  })
})
