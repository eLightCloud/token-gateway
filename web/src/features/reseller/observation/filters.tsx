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

import { useId, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { Combobox } from '@/components/ui/combobox'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'

import type { Customer } from './api'
import {
  observationDates,
  observationDay,
  type ObservationSearch,
  type ObservationView,
} from './routes'

export function ObservationFilters(props: {
  customers: Customer[]
  view: ObservationView
  filters: ObservationSearch & { from: number; until: number }
  onApply: (filters: ObservationSearch) => void
  onReset: () => void
}) {
  const { t } = useTranslation()
  const id = useId()
  const [customer, setCustomer] = useState(
    String(props.filters.customer_id ?? 'all')
  )
  const [start, setStart] = useState(() =>
    new Date(props.filters.from * 1000).toISOString().slice(0, 10)
  )
  const [end, setEnd] = useState(() =>
    new Date((props.filters.until - 1) * 1000).toISOString().slice(0, 10)
  )
  const [model, setModel] = useState(props.filters.model ?? '')
  const [identifier, setIdentifier] = useState(
    props.filters.request_id ?? props.filters.task_id ?? ''
  )
  const [kind, setKind] = useState<
    NonNullable<ObservationSearch['kind']> | 'all'
  >(props.filters.kind ?? 'all')
  const [invalid, setInvalid] = useState(false)
  const hasModel = props.view === 'usage' || props.view === 'analytics'
  const hasIdentifier = props.view === 'usage' || props.view === 'tasks'
  const results = {
    all: t('All'),
    consumption: t('Consumption'),
    failure: t('Failed'),
  }
  const options = [
    { value: 'all', label: t('All my customers') },
    ...props.customers.map((row) => ({
      value: String(row.user_id),
      label: row.username,
    })),
  ]

  return (
    <form
      aria-label={t('Filters')}
      className='flex flex-wrap items-end gap-3'
      onSubmit={(event) => {
        event.preventDefault()
        const from = Date.parse(start) / 1000
        const until = Date.parse(end) / 1000 + observationDay
        const valid =
          from > 0 &&
          until > from &&
          until - from <= 31 * observationDay &&
          until <= observationDates().until
        setInvalid(!valid)
        if (!valid) return
        props.onApply({
          customer_id: customer === 'all' ? undefined : Number(customer),
          from,
          until,
          model: hasModel ? model.trim() || undefined : undefined,
          request_id:
            props.view === 'usage' ? identifier.trim() || undefined : undefined,
          task_id:
            props.view === 'tasks' ? identifier.trim() || undefined : undefined,
          kind: kind === 'all' ? undefined : kind,
        })
      }}
    >
      <div className='min-w-40 space-y-1'>
        <Label htmlFor={`${id}-customer`}>{t('Customer')}</Label>
        <Combobox
          id={`${id}-customer`}
          options={options}
          value={customer}
          onValueChange={(value) => setCustomer(value ?? 'all')}
          placeholder={t('All my customers')}
          emptyText={t('No results')}
        />
      </div>
      <div className='space-y-1'>
        <Label htmlFor={`${id}-start`}>{t('Start date')} (UTC)</Label>
        <Input
          id={`${id}-start`}
          type='date'
          required
          value={start}
          onChange={(event) => setStart(event.target.value)}
        />
      </div>
      <div className='space-y-1'>
        <Label htmlFor={`${id}-end`}>{t('End date')} (UTC)</Label>
        <Input
          id={`${id}-end`}
          type='date'
          required
          value={end}
          onChange={(event) => setEnd(event.target.value)}
        />
      </div>
      {hasModel && (
        <div className='space-y-1'>
          <Label htmlFor={`${id}-model`}>{t('Model')}</Label>
          <Input
            id={`${id}-model`}
            value={model}
            maxLength={512}
            onChange={(event) => setModel(event.target.value)}
          />
        </div>
      )}
      {hasIdentifier && (
        <div className='space-y-1'>
          <Label htmlFor={`${id}-identifier`}>
            {props.view === 'usage' ? t('Request ID') : t('Task ID')}
          </Label>
          <Input
            id={`${id}-identifier`}
            value={identifier}
            maxLength={props.view === 'usage' ? 64 : 255}
            onChange={(event) => setIdentifier(event.target.value)}
          />
        </div>
      )}
      {props.view === 'usage' && (
        <div className='space-y-1'>
          <Label htmlFor={`${id}-kind`}>{t('Result')}</Label>
          <Select
            value={kind}
            onValueChange={(value) => {
              if (
                value === 'all' ||
                value === 'consumption' ||
                value === 'failure'
              ) {
                setKind(value)
              }
            }}
          >
            <SelectTrigger id={`${id}-kind`}>
              <SelectValue>
                {results[kind === 'drawing' || kind === 'async' ? 'all' : kind]}
              </SelectValue>
            </SelectTrigger>
            <SelectContent>
              <SelectItem value='all'>{results.all}</SelectItem>
              <SelectItem value='consumption'>{results.consumption}</SelectItem>
              <SelectItem value='failure'>{results.failure}</SelectItem>
            </SelectContent>
          </Select>
        </div>
      )}
      <Button type='submit'>{t('Search')}</Button>
      <Button type='button' variant='outline' onClick={props.onReset}>
        {t('Reset')}
      </Button>
      {invalid && (
        <p role='alert' className='text-destructive basis-full text-sm'>
          {t('Select up to 31 days, ending no later than today.')}
        </p>
      )}
    </form>
  )
}
