import { AxiosError } from 'axios'
import { afterEach, describe, expect, it } from 'vitest'

import { api } from '@/lib/http-client'

import { loadHealth } from '../api'

const originalAdapter = api.defaults.adapter

afterEach(() => {
  api.defaults.adapter = originalAdapter
})

describe('model health reads', () => {
  it('loads capabilities when a replacement query starts before a cancelled query settles', async () => {
    const previous = new AbortController()
    previous.abort()
    api.defaults.adapter = async (config) => ({
      data: { success: true, data: { worker: false } },
      status: 200,
      statusText: 'OK',
      headers: {},
      config,
    })

    const cancelled = loadHealth('/capabilities', undefined, previous.signal)
    const replacement = loadHealth(
      '/capabilities',
      undefined,
      new AbortController().signal
    )

    await expect(cancelled).rejects.toHaveProperty(
      'code',
      AxiosError.ERR_CANCELED
    )
    await expect(replacement).resolves.toEqual({ worker: false })
  })

  it('reports a rejected overview instead of presenting an empty model list', async () => {
    api.defaults.adapter = async (config) => ({
      data: { success: false, message: 'Catalog unavailable' },
      status: 200,
      statusText: 'OK',
      headers: {},
      config,
    })

    await expect(loadHealth('/overview')).rejects.toThrow('Catalog unavailable')
  })
})
