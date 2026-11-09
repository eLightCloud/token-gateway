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

import { Field, FieldError } from '@/components/ui/field'
import { Input } from '@/components/ui/input'

import type { ConfigItem } from './api'

export function ResellerRatioField(props: {
  item: ConfigItem
  invalid: boolean
  disabled: boolean
  onChange: (value: string) => void
}) {
  const { t } = useTranslation()
  const errorID = useId()
  const invalid = props.invalid

  return (
    <Field data-invalid={invalid} className='gap-1 whitespace-normal'>
      <Input
        aria-label={t('Ratio for {{model}} channel {{channel}}', {
          model: props.item.model,
          channel: props.item.channel_id,
        })}
        aria-invalid={invalid}
        aria-describedby={invalid ? errorID : undefined}
        className='max-w-28'
        inputMode='decimal'
        value={props.item.ratio}
        disabled={props.disabled}
        onChange={(event) => props.onChange(event.target.value)}
      />
      {invalid && (
        <FieldError id={errorID} className='max-w-56 text-xs'>
          {t(
            'Enter a ratio greater than 0 and at most 2, with up to six decimal places.'
          )}
        </FieldError>
      )}
    </Field>
  )
}
