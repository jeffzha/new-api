import { getLobeIcon } from '@/lib/lobe-icon'
import { resolveModelProvider } from '@/lib/model-provider'

export function HealthModelName(props: { model: string }) {
  const provider = resolveModelProvider(props.model)

  return (
    <div className='flex items-start gap-2 font-semibold'>
      {provider && (
        <span
          aria-hidden='true'
          title={provider.label ?? provider.name}
          className='mt-0.5 flex size-[18px] shrink-0 items-center justify-center'
        >
          {getLobeIcon(provider.icon, 18)}
        </span>
      )}
      <span className='min-w-0 [overflow-wrap:anywhere]'>{props.model}</span>
    </div>
  )
}
