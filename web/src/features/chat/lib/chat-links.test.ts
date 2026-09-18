/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.
*/
import { describe, expect, it } from 'vitest'

import {
  isChatControlLink,
  parseChatConfig,
  resolveChatUrl,
} from './chat-links'

describe('chat link resolution', () => {
  it('does not expose the legacy CC Switch control marker as a URL', () => {
    expect(isChatControlLink('ccswitch')).toBe(true)
    expect(isChatControlLink('CCSWITCH')).toBe(true)
    expect(
      parseChatConfig([
        { 'CC Switch': 'ccswitch' },
        { Cherry: 'cherrystudio://providers/api-keys?v=1&data={cherryConfig}' },
      ])
    ).toMatchObject([{ id: '0', name: 'Cherry' }])
  })

  it('builds a Cherry Studio protocol URL with the active key', () => {
    const url = resolveChatUrl({
      template: 'cherrystudio://providers/api-keys?v=1&data={cherryConfig}',
      apiKey: 'sk-example',
      serverAddress: 'https://gateway.example.com',
    })

    expect(url.startsWith('cherrystudio://providers/api-keys?v=1&data=')).toBe(
      true
    )
    expect(url).toContain(
      encodeURIComponent(
        Buffer.from(
          JSON.stringify({
            id: 'new-api',
            baseUrl: 'https://gateway.example.com',
            apiKey: 'sk-example',
          }),
          'utf8'
        ).toString('base64')
      )
    )
  })

  it('encodes web chat provider settings as one URL-safe JSON value', () => {
    const lobeUrl = resolveChatUrl({
      template:
        'https://chat-preview.lobehub.com/?settings={\"keyVaults\":{\"openai\":{\"apiKey\":\"{key}\",\"baseURL\":\"{address}/v1\"}}}',
      apiKey: 'sk-example+special',
      serverAddress: 'https://gateway.example.com/',
    })
    expect(new URL(lobeUrl).searchParams.get('settings')).toBe(
      JSON.stringify({
        keyVaults: {
          openai: {
            apiKey: 'sk-example+special',
            baseURL: 'https://gateway.example.com/v1',
          },
        },
      })
    )

    const aiawUrl = resolveChatUrl({
      template:
        'https://aiaw.app/set-provider?provider={\"type\":\"openai\",\"settings\":{\"apiKey\":\"{key}\",\"baseURL\":\"{address}/v1\",\"compatibility\":\"strict\"}}',
      apiKey: 'sk-example',
      serverAddress: 'https://gateway.example.com/',
    })
    expect(new URL(aiawUrl).searchParams.get('provider')).toBe(
      JSON.stringify({
        type: 'openai',
        settings: {
          apiKey: 'sk-example',
          baseURL: 'https://gateway.example.com/v1',
          compatibility: 'strict',
        },
      })
    )
  })

  it('escapes API keys in AMA and OpenCat query links', () => {
    const key = 'sk-example+with&reserved=chars'
    const serverAddress = 'https://gateway.example.com/'

    const ama = new URL(
      resolveChatUrl({
        template: 'ama://set-api-key?server={address}&key={key}',
        apiKey: key,
        serverAddress,
      })
    )
    expect(ama.searchParams.get('server')).toBe('https://gateway.example.com')
    expect(ama.searchParams.get('key')).toBe(key)

    const openCat = new URL(
      resolveChatUrl({
        template: 'opencat://team/join?domain={address}&token={key}',
        apiKey: key,
        serverAddress,
      })
    )
    expect(openCat.searchParams.get('domain')).toBe(
      'https://gateway.example.com'
    )
    expect(openCat.searchParams.get('token')).toBe(key)
  })

  it('builds valid desktop protocol links for AionUI, DeepChat, and AQBot', () => {
    const serverAddress = 'https://gateway.example.com/'
    const apiKey = 'sk-example'
    const protocolTemplates = [
      [
        'aionui://provider/add?v=1&data={aionuiConfig}',
        'aionui://provider/add?v=1&data=',
      ],
      [
        'deepchat://provider/install?v=1&data={deepchatConfig}',
        'deepchat://provider/install?v=1&data=',
      ],
    ] as const

    for (const [template, prefix] of protocolTemplates) {
      const resolved = resolveChatUrl({ template, apiKey, serverAddress })
      expect(resolved.startsWith(prefix)).toBe(true)
      expect(resolved).not.toContain('{')
      expect(resolved).not.toContain('}')
    }

    const aqbot = resolveChatUrl({
      template: 'aqbot://providers?{aqbotConfig}',
      apiKey,
      serverAddress,
    })
    const aqbotQuery = new URL(aqbot).searchParams
    expect(aqbotQuery.get('name')).toBe('New API')
    expect(aqbotQuery.get('baseurl')).toBe('https://gateway.example.com')
    expect(aqbotQuery.get('apikey')).toBe(apiKey)
    expect(aqbotQuery.get('type')).toBe('openai')
  })
})
