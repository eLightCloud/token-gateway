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
import { zodResolver } from '@hookform/resolvers/zod'
import { useEffect, useState } from 'react'
import { Controller, useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { Dialog } from '@/components/dialog'
import { MultiSelect } from '@/components/multi-select'
import { Button } from '@/components/ui/button'
import { Combobox } from '@/components/ui/combobox'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'

import type { PricingRule, PricingWorkspace } from './api'
import { ruleSchema } from './lib/rule-schema'
import { PricingRatioLabel } from './ratio-label'

export function PricingRuleDialog(props: {
  rule: PricingRule
  creating: boolean
  models: string[]
  candidates: PricingWorkspace['candidates']
  onClose: () => void
  onApply: (rule: PricingRule) => void
  onDirtyChange: (dirty: boolean) => void
  configuredRules?: PricingRule[]
  onSelectConfigured?: (rule: PricingRule) => void
}) {
  const { t } = useTranslation()
  const form = useForm<PricingRule>({
    defaultValues: props.rule,
    resolver: zodResolver(ruleSchema),
    mode: 'onChange',
  })
  const [filter, setFilter] = useState('')
  const [discard, setDiscard] = useState(false)
  const model = form.watch('model')
  const selected = form.watch('channel_ids')
  const ratio = form.watch('ratio')
  const dirty = form.formState.isDirty
  const onDirtyChange = props.onDirtyChange
  useEffect(() => {
    onDirtyChange(dirty)
  }, [dirty, onDirtyChange])
  const options = props.candidates
    .filter(
      (c) => c.models.split(',').includes(model) || selected.includes(c.id)
    )
    .map((c) => ({
      value: String(c.id),
      label: `${c.name || '#'} #${c.id}`,
      hint:
        c.status === 1 && c.models.split(',').includes(model)
          ? undefined
          : t('Unavailable'),
    }))
  for (const id of selected) {
    if (!options.some((o) => o.value === String(id))) {
      options.push({
        value: String(id),
        label: `#${id}`,
        hint: t('Unavailable'),
      })
    }
  }
  const matching = options.filter((o) =>
    o.label.toLowerCase().includes(filter.toLowerCase())
  )
  return (
    <>
      <Dialog
        open
        onOpenChange={(open) => {
          if (!open) {
            if (dirty) setDiscard(true)
            else props.onClose()
          }
        }}
        title={t('Edit realtime model rule')}
      >
        <form className='space-y-4' onSubmit={form.handleSubmit(props.onApply)}>
          <div className='space-y-2'>
            <Label htmlFor='realtime-model'>{t('Model')}</Label>
            <Controller
              control={form.control}
              name='model'
              render={({ field }) => (
                <Combobox
                  id='realtime-model'
                  value={field.value}
                  disabled={!props.creating}
                  options={(props.creating
                    ? props.models
                    : [props.rule.model]
                  ).map((m) => ({
                    value: m,
                    label:
                      props.creating &&
                      props.configuredRules?.some((r) => r.model === m)
                        ? `${m} · ${t('Configured')}`
                        : m,
                  }))}
                  onValueChange={(value) => {
                    const configured = props.configuredRules?.find(
                      (r) => r.model === value
                    )
                    if (configured && props.onSelectConfigured) {
                      props.onSelectConfigured(configured)
                      return
                    }
                    field.onChange(value ?? '')
                    form.setValue('channel_ids', [], {
                      shouldDirty: true,
                      shouldValidate: true,
                    })
                  }}
                  placeholder={t('Select model')}
                />
              )}
            />
          </div>
          <div className='space-y-2'>
            <Label htmlFor='realtime-channel-search'>
              {t('Search channels')}
            </Label>
            <Input
              id='realtime-channel-search'
              value={filter}
              onChange={(e) => setFilter(e.target.value)}
              disabled={!model}
            />
          </div>
          <div className='flex gap-2'>
            <Button
              type='button'
              variant='outline'
              disabled={!model}
              onClick={() =>
                form.setValue(
                  'channel_ids',
                  [
                    ...new Set([
                      ...selected,
                      ...matching.map((c) => Number(c.value)),
                    ]),
                  ],
                  { shouldDirty: true, shouldValidate: true }
                )
              }
            >
              {t('Select matching channels')}
            </Button>
            <Button
              type='button'
              variant='outline'
              onClick={() =>
                form.setValue('channel_ids', [], {
                  shouldDirty: true,
                  shouldValidate: true,
                })
              }
            >
              {t('Clear')}
            </Button>
          </div>
          <div className='space-y-2'>
            <Label htmlFor='realtime-channels'>
              {t('Applicable channels')}
            </Label>
            <Controller
              control={form.control}
              name='channel_ids'
              render={({ field }) => (
                <MultiSelect
                  id='realtime-channels'
                  placeholder={t('Applicable channels')}
                  disabled={!model}
                  options={matching}
                  displayOptions={options}
                  selected={field.value.map(String)}
                  onChange={(values) => field.onChange(values.map(Number))}
                  maxVisibleChips={3}
                />
              )}
            />
          </div>
          <div className='space-y-2'>
            <Label htmlFor='realtime-ratio'>{t('Ratio')}</Label>
            <Input
              id='realtime-ratio'
              inputMode='decimal'
              aria-invalid={!!form.formState.errors.ratio}
              aria-describedby='realtime-ratio-hint'
              {...form.register('ratio')}
            />
            <p id='realtime-ratio-hint'>
              {t(
                'A ratio of 1 uses the base price on selected channels when the rule is enabled.'
              )}
            </p>
            {ruleSchema.shape.ratio.safeParse(ratio).success && (
              <p role='status'>
                <PricingRatioLabel ratio={ratio} />
              </p>
            )}
          </div>
          {!form.formState.isValid && (
            <p role='alert'>
              {t(
                'Choose a model and at least one channel. Enter a ratio above 0 and at most 5 with up to two decimal places.'
              )}
            </p>
          )}
          <Button disabled={!form.formState.isValid} type='submit'>
            {t('Apply to draft')}
          </Button>
        </form>
      </Dialog>
      <ConfirmDialog
        open={discard}
        onOpenChange={setDiscard}
        title={t('Unsaved changes')}
        desc={t('Discard this rule edit?')}
        handleConfirm={props.onClose}
      />
    </>
  )
}
