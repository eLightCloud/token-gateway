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

import { LoadingState } from '@/components/loading-state'
import { formatNumber } from '@/lib/format'

import { formatTimestampInBeijingTime } from './beijing-time'

export function OrganizationUsageBuildNotice(props: {
  sliceDone: number
  sliceTotal: number
  coveredUntil?: number
}) {
  const { t } = useTranslation()
  return (
    <div
      role='status'
      aria-live='polite'
      className='text-muted-foreground space-y-1 rounded-lg border p-3 text-sm'
    >
      <LoadingState
        inline
        size='sm'
        message={t('Preparing usage statistics...')}
      />
      <p>
        {t('This query will update automatically when processing finishes.')}
      </p>
      {props.sliceTotal > 0 ? (
        <p>
          {t('Prepared slices')}: {formatNumber(props.sliceDone)}/
          {formatNumber(props.sliceTotal)}
        </p>
      ) : null}
      {props.coveredUntil ? (
        <p>
          {t('Coverage through')}:{' '}
          {formatTimestampInBeijingTime(props.coveredUntil)} (
          {t('Beijing Time')})
        </p>
      ) : null}
    </div>
  )
}
