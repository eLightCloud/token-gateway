import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { getServerErrorMessage } from '@/lib/server-error-message'

export function PersonalLogStatsAction(props: {
  busy: boolean
  error: Error | null
  onCalculate: () => void
  children: ReactNode
}) {
  const { t } = useTranslation()
  return (
    <div className='flex flex-wrap items-center gap-2'>
      <Button
        variant='outline'
        size='sm'
        disabled={props.busy}
        onClick={props.onCalculate}
      >
        {t('Calculate statistics for selected range')}
      </Button>
      {props.busy && <span role='status'>{t('Loading...')}</span>}
      {props.error && (
        <span role='status' className='text-muted-foreground text-sm'>
          {getServerErrorMessage(props.error, t('Failed to query statistics'))}
        </span>
      )}
      {props.children}
    </div>
  )
}
