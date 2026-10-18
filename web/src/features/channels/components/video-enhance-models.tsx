/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { useTranslation } from 'react-i18next'

import { CopyButton } from '@/components/copy-button'
import { Badge } from '@/components/ui/badge'

import { getVideoEnhanceBaseModels } from '../lib/video-enhance'

export function VideoEnhanceModels(props: {
  models: readonly string[]
  enabled: boolean
}) {
  const { t } = useTranslation()
  if (!props.enabled) return null
  const models = getVideoEnhanceBaseModels(props.models)
  return (
    <section
      aria-label={t('Enhancement models')}
      className='bg-muted/30 min-w-0 space-y-2 rounded-lg border p-3'
    >
      <p className='text-sm font-medium'>{t('Enhancement models')}</p>
      <p className='text-muted-foreground text-xs'>
        {t(
          'Automatically derived from the selected base models. No manual addition or upstream synchronization is needed.'
        )}
      </p>
      {models.length > 0 ? (
        <ul className='grid min-w-0 gap-2'>
          {models.map((base) => (
            <li
              key={base}
              className='flex min-w-0 items-start justify-between gap-2'
            >
              <Badge
                variant='secondary'
                className='h-auto min-w-0 shrink py-1 whitespace-normal'
              >
                <code className='min-w-0 break-all'>{base}-enhance</code>
              </Badge>
              <CopyButton
                value={`${base}-enhance`}
                size='icon'
                className='size-7'
                tooltip={t('Copy model name')}
              />
            </li>
          ))}
        </ul>
      ) : (
        <p role='status' className='text-muted-foreground text-xs'>
          {t(
            'Add a supported Seedance 2.0 or 2.5 base model to publish enhancement models.'
          )}
        </p>
      )}
      <p className='text-muted-foreground text-xs'>
        {t(
          'Calls require the enhancement service and model pricing to be configured.'
        )}
      </p>
    </section>
  )
}
