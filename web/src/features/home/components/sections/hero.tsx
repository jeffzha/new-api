import { Link } from '@tanstack/react-router'
import { ArrowRight, BookOpen, CheckCircle2, ShieldCheck, WalletCards } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { useStatus } from '@/hooks/use-status'

import { HeroTerminalDemo } from '../hero-terminal-demo'

interface HeroProps {
  className?: string
  isAuthenticated?: boolean
}

export function Hero({ isAuthenticated }: HeroProps) {
  const { t } = useTranslation()
  const { status } = useStatus()
  const docsUrl = (status?.docs_link as string | undefined) || '/docs'
  const capabilities = [
    { icon: ShieldCheck, title: '稳定接入', description: '统一鉴权、故障切换与调用状态追踪' },
    { icon: WalletCards, title: '透明计费', description: '模型价格、消费记录与余额变化清晰可查' },
    { icon: CheckCircle2, title: '兼容常用协议', description: '接入 OpenAI 兼容客户端与现有业务系统' },
  ]

  return (
    <section className='border-border bg-background border-b px-6 pt-20 pb-14 md:pt-28 md:pb-20'>
      <div className='mx-auto max-w-6xl'>
        <div className='mb-12 max-w-3xl'>
          <p className='text-primary mb-4 text-sm font-semibold'>Nexus Reach AI Gateway</p>
          <h1 className='text-foreground text-4xl leading-tight font-semibold md:text-6xl'>
            稳定、透明的企业级 AI 模型服务
          </h1>
          <p className='text-muted-foreground mt-5 max-w-2xl text-base leading-7 md:text-lg'>
            一个 API 连接主流文本、图像与视频模型。按量计费、统一账单，并提供清晰的调用记录与用量管理。
          </p>
          <div className='mt-8 flex flex-wrap gap-3'>
            <Button className='h-11 px-5' render={<Link to={isAuthenticated ? '/dashboard' : '/sign-up'} />}>
              {isAuthenticated ? t('Go to Dashboard') : t('Get Started')}
              <ArrowRight className='ml-2 size-4' />
            </Button>
            <Button variant='outline' className='h-11 px-5' render={<Link to='/pricing' />}>
              {t('View Pricing')}
            </Button>
            <Button
              variant='ghost'
              className='h-11 px-4'
              render={docsUrl.startsWith('http') ? <a href={docsUrl} target='_blank' rel='noreferrer' /> : <Link to={docsUrl} />}
            >
              <BookOpen className='mr-2 size-4' />
              {t('Docs')}
            </Button>
          </div>
        </div>

        <div className='grid gap-10 lg:grid-cols-[0.8fr_1.2fr] lg:items-center'>
          <div className='divide-border border-border grid divide-y border-y'>
            {capabilities.map(({ icon: Icon, title, description }) => (
              <div key={title} className='flex gap-4 py-5'>
                <Icon className='text-primary mt-0.5 size-5 shrink-0' />
                <div>
                  <h2 className='text-sm font-semibold'>{title}</h2>
                  <p className='text-muted-foreground mt-1 text-sm leading-6'>{description}</p>
                </div>
              </div>
            ))}
          </div>
          <HeroTerminalDemo />
        </div>
      </div>
    </section>
  )
}
