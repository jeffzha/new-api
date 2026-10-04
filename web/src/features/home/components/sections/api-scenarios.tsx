/* Copyright (C) 2023-2026 QuantumNous
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */
import { Link } from '@tanstack/react-router'
import {
  ArrowRight,
  Check,
  ImageIcon,
  MessageSquare,
  Video,
} from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'

import { ShowcaseVideo } from '../showcase-video'

export function ApiScenarios() {
  const { t } = useTranslation()
  const scenarios = [
    {
      id: 'chat',
      icon: MessageSquare,
      label: t('Chat API'),
      title: t('Build conversations that understand context'),
      description: t(
        'Connect assistants, knowledge tools, and coding workflows to language models through compatible APIs.'
      ),
      capabilities: [
        t('Streaming responses'),
        t('Tool calling'),
        t('Multimodal input'),
      ],
      visual: (
        <div className='gateway-chat-example'>
          <div className='gateway-example-top'>
            <MessageSquare size={17} aria-hidden='true' />
            <span>{t('Conversation example')}</span>
          </div>
          <div className='gateway-chat-flow'>
            <div className='gateway-chat-question'>
              {t('How can I connect my application?')}
            </div>
            <div className='gateway-chat-answer'>
              <span className='gateway-assistant-icon'>
                <MessageSquare size={18} aria-hidden='true' />
              </span>
              <p>
                {t(
                  'Create an API key, choose a model, and follow the documentation to configure your endpoint.'
                )}
              </p>
            </div>
            <div className='gateway-example-protocol'>
              <code>POST /v1/chat/completions</code>
              <span>SSE / JSON</span>
            </div>
          </div>
        </div>
      ),
    },
    {
      id: 'image',
      icon: ImageIcon,
      label: t('Image API'),
      title: t('Make creativity part of your workflow'),
      description: t(
        'Connect image generation and editing to your products, from marketing visuals to design exploration.'
      ),
      capabilities: [
        t('Text to image'),
        t('Reference images'),
        t('Image editing'),
      ],
      visual: (
        <img
          className='gateway-scenario-image'
          src='/images/home/image-api.webp'
          alt={t('Make creativity part of your workflow')}
          width='1536'
          height='1024'
          loading='lazy'
        />
      ),
    },
    {
      id: 'video',
      icon: Video,
      label: t('Video API'),
      title: t('From a single idea to moving stories'),
      description: t(
        'Submit generation tasks, follow their progress, and retrieve results through a consistent task workflow.'
      ),
      capabilities: [
        t('Text to video'),
        t('Image to video'),
        t('Task tracking'),
      ],
      visual: (
        <ShowcaseVideo
          src='/images/home/coast.mp4'
          poster='/images/home/coast.webp'
          label={t('Coastal landscape creative illustration')}
        />
      ),
    },
  ]
  return (
    <section
      className='gateway-scenarios gateway-section'
      aria-labelledby='gateway-scenarios-title'
    >
      <div className='gateway-container'>
        <div className='gateway-section-heading'>
          <div>
            <h2 id='gateway-scenarios-title'>
              {t('An API for every kind of idea')}
            </h2>
          </div>
        </div>
        <Tabs defaultValue='chat'>
          <TabsList
            className='gateway-scenario-tabs'
            aria-label={t('API scenarios')}
          >
            {scenarios.map((scenario) => (
              <TabsTrigger
                key={scenario.id}
                value={scenario.id}
                icon={<scenario.icon size={17} />}
              >
                {scenario.label}
              </TabsTrigger>
            ))}
          </TabsList>
          {scenarios.map((scenario) => (
            <TabsContent key={scenario.id} value={scenario.id}>
              <div className='gateway-scenario-layout'>
                <div className='gateway-scenario-copy'>
                  <h3>{scenario.title}</h3>
                  <p>{scenario.description}</p>
                  <ul>
                    {scenario.capabilities.map((capability) => (
                      <li key={capability}>
                        <Check size={15} aria-hidden='true' />
                        {capability}
                      </li>
                    ))}
                  </ul>
                  <Button variant='outline' render={<Link to='/docs' />}>
                    {t('Open API documentation')}
                    <ArrowRight size={16} aria-hidden='true' />
                  </Button>
                </div>
                <div className='gateway-scenario-visual'>{scenario.visual}</div>
              </div>
            </TabsContent>
          ))}
        </Tabs>
        <p className='gateway-scenario-note'>
          {t(
            'Capabilities vary by model. Check the model catalog and documentation before integration.'
          )}
        </p>
      </div>
    </section>
  )
}
