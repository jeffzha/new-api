import { act, renderHook } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'

import { usePlaygroundConversation } from '../use-playground-conversation'

describe('playground conversation capabilities', () => {
  it('does not append or send a message when the selected model cannot chat', () => {
    const sendChat = vi.fn()
    const updateMessages = vi.fn()
    const { result } = renderHook(() =>
      usePlaygroundConversation({
        messages: [],
        updateMessages,
        sendChat,
        canSend: false,
      })
    )
    act(() => result.current.handleSendMessage('hello'))
    expect(updateMessages).not.toHaveBeenCalled()
    expect(sendChat).not.toHaveBeenCalled()
  })

  it('sends a message when the selected model supports chat', () => {
    const sendChat = vi.fn()
    const updateMessages = vi.fn()
    const { result } = renderHook(() =>
      usePlaygroundConversation({
        messages: [],
        updateMessages,
        sendChat,
        canSend: true,
      })
    )
    act(() => result.current.handleSendMessage('hello'))
    expect(sendChat).toHaveBeenCalledWith(
      expect.arrayContaining([expect.objectContaining({ from: 'user' })])
    )
  })
})
