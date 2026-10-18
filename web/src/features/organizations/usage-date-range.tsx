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
import { useId } from 'react'
import { useTranslation } from 'react-i18next'

import {
  Field,
  FieldError,
  FieldGroup,
  FieldLabel,
} from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { cn } from '@/lib/utils'

import type { useUsageDateRange } from './use-usage-date-range'

export function UsageDateRangeFields(props: {
  range: ReturnType<typeof useUsageDateRange>
  onChange?: () => void
  className?: string
}) {
  const { t } = useTranslation()
  const id = useId()
  const errorId = `${id}-error`
  let error: string | undefined
  if (props.range.dateRangeError === 'invalid') {
    error = t('Please enter a valid date')
  } else if (props.range.dateRangeError === 'reversed') {
    error = t('Start date cannot be after end date')
  } else if (props.range.dateRangeError === 'future') {
    error = t('Dates cannot be in the future')
  }
  const startMax =
    props.range.endDate && props.range.endDate < props.range.latestDate
      ? props.range.endDate
      : props.range.latestDate

  return (
    <FieldGroup className={cn('contents', props.className)}>
      <Field className='gap-1' data-invalid={!props.range.isDateRangeValid}>
        <FieldLabel
          htmlFor={`${id}-start`}
          className='text-muted-foreground text-xs'
        >
          {t('Start date')} ({t('Beijing Time')})
        </FieldLabel>
        <Input
          id={`${id}-start`}
          type='date'
          value={props.range.startDate}
          max={startMax}
          aria-invalid={!props.range.isDateRangeValid}
          aria-describedby={error ? errorId : undefined}
          onChange={(event) => {
            props.range.setStartDate(event.target.value)
            props.onChange?.()
          }}
        />
      </Field>
      <Field className='gap-1' data-invalid={!props.range.isDateRangeValid}>
        <FieldLabel
          htmlFor={`${id}-end`}
          className='text-muted-foreground text-xs'
        >
          {t('End date')} ({t('Beijing Time')})
        </FieldLabel>
        <Input
          id={`${id}-end`}
          type='date'
          value={props.range.endDate}
          min={props.range.startDate || undefined}
          max={props.range.latestDate}
          aria-invalid={!props.range.isDateRangeValid}
          aria-describedby={error ? errorId : undefined}
          onChange={(event) => {
            props.range.setEndDate(event.target.value)
            props.onChange?.()
          }}
        />
      </Field>
      <FieldError id={errorId} className='order-last col-span-full basis-full'>
        {error}
      </FieldError>
    </FieldGroup>
  )
}
