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
import { useMutation } from '@tanstack/react-query'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { v4 as uuidv4 } from 'uuid'

import { Dialog } from '@/components/dialog'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { RadioGroup, RadioGroupItem } from '@/components/ui/radio-group'
import { toIntlLocale } from '@/i18n/languages'
import { formatQuotaWithCurrency } from '@/lib/currency'
import { parseQuotaFromDollars } from '@/lib/format'

import { resellerPath, resellerRequest } from './api'

export function ResellerQuotaDialog({
  userID,
  username,
  admin,
  quota,
  open,
  onOpenChange,
  onSuccess,
}: {
  userID: number
  username?: string
  admin: boolean
  quota: number
  open: boolean
  onOpenChange: (open: boolean) => void
  onSuccess: () => void
}) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const [mode, setMode] = useState<'add' | 'subtract'>('add')
  const [amount, setAmount] = useState('')
  const [reason, setReason] = useState('')
  const [operation, setOperation] = useState(() => uuidv4())
  const quantity = parseQuotaFromDollars(Number(amount))
  const valid =
    Number.isSafeInteger(quantity) && quantity > 0 && reason.trim().length > 0
  const mutation = useMutation({
    meta: { errorToast: false },
    mutationFn: () =>
      resellerRequest(
        `${resellerPath(admin)}/${userID}/quota-adjustments`,
        'post',
        { operation_id: operation, mode, quota: quantity, reason }
      ),
    onSuccess: () => {
      onSuccess()
      onOpenChange(false)
    },
  })
  // A changed payload needs a new id; retries of an unchanged payload retain it.
  function change() {
    setOperation(uuidv4())
    mutation.reset()
  }
  return (
    <Dialog
      open={open}
      onOpenChange={(open) => {
        if (!mutation.isPending) onOpenChange(open)
      }}
      title={
        username ? `${t('Adjust Quota')} · ${username}` : t('Adjust Quota')
      }
      description={
        admin
          ? undefined
          : t(
              'Allocation does not deduct procurement quota. The server confirms the final balance.'
            )
      }
      footer={
        <>
          <Button
            variant='outline'
            onClick={() => onOpenChange(false)}
            disabled={mutation.isPending}
          >
            {t('Cancel')}
          </Button>
          <Button
            disabled={!valid || mutation.isPending}
            onClick={() => mutation.mutate()}
          >
            {t('Confirm')}
          </Button>
        </>
      }
    >
      <div className='space-y-4'>
        <fieldset disabled={mutation.isPending}>
          <legend id='reseller-quota-operation'>{t('Operation')}</legend>
          <RadioGroup
            aria-labelledby='reseller-quota-operation'
            className='flex gap-4'
            value={mode}
            disabled={mutation.isPending}
            onValueChange={(value) => {
              if (value !== 'add' && value !== 'subtract') return
              setMode(value)
              change()
            }}
          >
            <div className='flex items-center gap-2'>
              <RadioGroupItem id='reseller-quota-add' value='add' />
              <Label htmlFor='reseller-quota-add'>{t('Add')}</Label>
            </div>
            <div className='flex items-center gap-2'>
              <RadioGroupItem id='reseller-quota-subtract' value='subtract' />
              <Label htmlFor='reseller-quota-subtract'>{t('Subtract')}</Label>
            </div>
          </RadioGroup>
        </fieldset>
        <div className='space-y-2'>
          <Label htmlFor='reseller-quota'>{t('Amount')}</Label>
          <Input
            id='reseller-quota'
            type='number'
            min='0'
            step='any'
            value={amount}
            disabled={mutation.isPending}
            onChange={(event) => {
              setAmount(event.target.value)
              change()
            }}
          />
        </div>
        <div className='space-y-2'>
          <Label htmlFor='reseller-reason'>{t('Reason')}</Label>
          <Input
            id='reseller-reason'
            value={reason}
            maxLength={1000}
            disabled={mutation.isPending}
            onChange={(event) => {
              setReason(event.target.value)
              change()
            }}
          />
        </div>
        {valid && (
          <p>
            {formatQuotaWithCurrency(quota, { locale })} →{' '}
            {formatQuotaWithCurrency(
              quota + (mode === 'add' ? quantity : -quantity),
              { locale }
            )}
          </p>
        )}
        {mutation.isError && (
          <p role='alert'>
            {t(
              'Quota adjustment failed. Check the current balance and try again.'
            )}
          </p>
        )}
      </div>
    </Dialog>
  )
}
