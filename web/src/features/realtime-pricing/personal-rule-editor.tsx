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
import { useEffect, useId, useMemo } from 'react'
import { useController } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import { MultiSelect } from '@/components/multi-select'
import { Combobox } from '@/components/ui/combobox'
import { Field, FieldError } from '@/components/ui/field'
import { Input } from '@/components/ui/input'

import type { PricingRule, PricingWorkspace } from './api'
import { PricingRatioLabel } from './ratio-label'
import type { PersonalRuleEdit } from './use-personal-rule-editor'

export function PersonalPricingModelPicker(props: {
  models: string[]
  rules: PricingRule[]
  disabled: boolean
  onSelect: (rule: PricingRule) => void
}) {
  const { t } = useTranslation()
  const options = useMemo(() => {
    const configured = new Set(props.rules.map((rule) => rule.model))
    return props.models.map((model) => ({
      value: model,
      label: model,
      description: configured.has(model) ? t('Configured') : undefined,
    }))
  }, [props.models, props.rules, t])
  return (
    <Combobox
      aria-label={t('Add model')}
      placeholder={t('Type a model name to add it')}
      className='w-full max-w-sm'
      disabled={props.disabled}
      options={options}
      matchFrom='start'
      onValueChange={(model: string | null) => {
        if (!model) return
        props.onSelect(
          props.rules.find((rule) => rule.model === model) ?? {
            model,
            ratio: '1',
            channel_ids: [],
          }
        )
      }}
    />
  )
}

export function PersonalPricingChannels(props: {
  edit: PersonalRuleEdit
  candidates: PricingWorkspace['candidates']
  disabled: boolean
}) {
  const { t } = useTranslation()
  const id = useId()
  const { field } = useController({
    control: props.edit.control,
    name: 'channel_ids',
  })
  const model = props.edit.model
  const selected = field.value
  const invalid = selected.length === 0
  useEffect(() => {
    if (props.edit.isNew && !props.disabled) {
      document.querySelector<HTMLInputElement>(`[id="${id}"]`)?.focus()
    }
  }, [id, props.edit.isNew, props.disabled])

  const options = useMemo(() => {
    const channels = props.candidates.flatMap((channel) => {
      const supportsModel = channel.models.split(',').includes(model)
      if (!supportsModel && !selected.includes(channel.id)) return []
      return [
        {
          value: String(channel.id),
          label: `${channel.name || '#'} #${channel.id}`,
          hint:
            supportsModel && channel.status === 1
              ? undefined
              : t('Unavailable'),
        },
      ]
    })
    const knownIDs = new Set(channels.map((channel) => channel.value))
    for (const id of selected) {
      if (!knownIDs.has(String(id))) {
        channels.push({
          value: String(id),
          label: `#${id}`,
          hint: t('Unavailable'),
        })
      }
    }
    return channels
  }, [props.candidates, model, selected, t])

  return (
    <Field
      className='min-w-0 gap-1 whitespace-normal'
      data-invalid={invalid || undefined}
    >
      <MultiSelect
        id={id}
        aria-invalid={invalid}
        aria-describedby={invalid ? `${id}-error` : undefined}
        placeholder={t('Applicable channels')}
        options={options}
        searchByLabel
        selected={selected.map(String)}
        onChange={(values) => field.onChange(values.map(Number))}
        disabled={props.disabled}
        maxVisibleChips={2}
      />
      {invalid && (
        <FieldError id={`${id}-error`}>
          {t('Please select at least one channel')}
        </FieldError>
      )}
    </Field>
  )
}

export function PersonalPricingRatio(props: {
  edit: PersonalRuleEdit
  disabled: boolean
}) {
  const { t } = useTranslation()
  const id = useId()
  const { field, fieldState } = useController({
    control: props.edit.control,
    name: 'ratio',
  })
  const invalid = !!fieldState.error
  useEffect(() => {
    if (!props.edit.isNew && !props.disabled) {
      document.querySelector<HTMLInputElement>(`[id="${id}"]`)?.focus()
    }
  }, [id, props.edit.isNew, props.disabled])
  return (
    <Field
      className='max-w-36 min-w-0 gap-1 whitespace-normal'
      data-invalid={invalid || undefined}
    >
      <Input
        {...field}
        id={id}
        className='max-w-24'
        aria-label={t('Ratio')}
        aria-invalid={invalid}
        aria-describedby={invalid ? `${id}-error` : undefined}
        disabled={props.disabled}
        inputMode='decimal'
      />
      {invalid ? (
        <FieldError id={`${id}-error`}>
          {t(
            'Enter a ratio above 0 and at most 5 with up to two decimal places.'
          )}
        </FieldError>
      ) : (
        <PricingRatioLabel ratio={field.value} />
      )}
    </Field>
  )
}
