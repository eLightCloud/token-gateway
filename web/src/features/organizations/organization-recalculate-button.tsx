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
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { Button } from '@/components/ui/button'

import { formatTimestampInBeijingTime } from './beijing-time'

export function OrganizationRecalculateButton(props: {
  disabled?: boolean
  pending: boolean
  scope: string
  coveredUntil?: number
  organizationId?: number
  kind: 'usage' | 'invoice'
  filters?: { userId: string; modelName: string; channelId: string }
  onConfirm: () => Promise<void>
}) {
  const { t } = useTranslation()
  const [open, setOpen] = useState(false)
  return (
    <>
      <Button
        variant='outline'
        size='sm'
        disabled={props.disabled || props.pending}
        aria-busy={props.pending}
        onClick={() => setOpen(true)}
      >
        {t('Force recalculation')}
      </Button>
      <ConfirmDialog
        open={open}
        onOpenChange={setOpen}
        title={t('Force recalculation')}
        desc={
          <div className='space-y-2'>
            <p>
              {t('Organization')}
              {props.organizationId ? ` #${props.organizationId}` : ''} ·{' '}
              {props.kind === 'invoice' ? t('Invoice') : t('Billing')}
            </p>
            <p>{props.scope}</p>
            {props.coveredUntil ? (
              <p>
                {t('Coverage through')}:{' '}
                {formatTimestampInBeijingTime(props.coveredUntil)} (
                {t('Beijing Time')})
              </p>
            ) : null}
            {props.filters ? (
              <p>
                {t('User')}: {props.filters.userId || t('All')} · {t('Model')}:{' '}
                {props.filters.modelName || t('All')} · {t('Channel')}:{' '}
                {props.filters.channelId || t('All')}
              </p>
            ) : null}
            <p>
              {t(
                'Recalculate the selected range and filters? This may take a long time. The last published result remains available while processing.'
              )}
            </p>
          </div>
        }
        confirmText={t('Confirm recalculation')}
        disabled={props.disabled}
        isLoading={props.pending}
        handleConfirm={() => {
          void props
            .onConfirm()
            .then(() => setOpen(false))
            .catch(() => {})
        }}
      />
    </>
  )
}
