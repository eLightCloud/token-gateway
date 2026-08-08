import { Calendar03Icon, SearchIcon } from '@hugeicons/core-free-icons'
import { HugeiconsIcon } from '@hugeicons/react'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import {
  InputGroup,
  InputGroupAddon,
  InputGroupInput,
} from '@/components/ui/input-group'
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import dayjs from '@/lib/dayjs'

import { BILL_TYPES } from '../constants'
import type { BillFilterOption, BillFilters, BillType } from '../types'

type BillFilterBarProps = {
  filters: BillFilters
  options?: {
    organizations: BillFilterOption[]
    models: BillFilterOption[]
    channels: BillFilterOption[]
  }
  onChange: (patch: Partial<BillFilters>) => void
}

function selectItems(
  allLabel: string,
  options: BillFilterOption[]
): Array<{ value: string; label: string }> {
  return [
    { value: 'all', label: allLabel },
    ...options.map((option) => ({
      value: String(option.value),
      label: option.label,
    })),
  ]
}

function BillSelect(props: {
  label: string
  value: string
  items: Array<{ value: string; label: string }>
  onValueChange: (value: string) => void
}) {
  return (
    <Select
      items={props.items}
      value={props.value}
      onValueChange={(value) => props.onValueChange(value ?? 'all')}
    >
      <SelectTrigger className='w-full justify-between md:min-w-44'>
        <span className='text-muted-foreground'>{props.label}:</span>
        <SelectValue />
      </SelectTrigger>
      <SelectContent alignItemWithTrigger={false}>
        <SelectGroup>
          {props.items.map((item) => (
            <SelectItem key={item.value} value={item.value}>
              {item.label}
            </SelectItem>
          ))}
        </SelectGroup>
      </SelectContent>
    </Select>
  )
}

export function BillFilterBar(props: BillFilterBarProps) {
  const { t } = useTranslation()
  const [requestId, setRequestId] = useState(props.filters.requestId ?? '')

  useEffect(() => {
    setRequestId(props.filters.requestId ?? '')
  }, [props.filters.requestId])

  const organizations = selectItems(
    t('All organizations'),
    props.options?.organizations ?? []
  )
  const models = selectItems(t('All models'), props.options?.models ?? [])
  const channels = selectItems(t('All channels'), props.options?.channels ?? [])
  const billTypes = BILL_TYPES.map((item) => ({
    value: item.value,
    label: t(item.labelKey),
  }))

  const submitSearch = () => {
    props.onChange({ requestId: requestId.trim() || undefined, page: 1 })
  }

  return (
    <div className='border-border bg-background grid gap-2 border-b p-3 md:grid-cols-2 xl:grid-cols-[repeat(4,minmax(0,1fr))_minmax(15rem,1.4fr)]'>
      {props.filters.perspective === 'customer' ? (
        <BillSelect
          label={t('Bill type')}
          value={props.filters.billType}
          items={billTypes}
          onValueChange={(value) =>
            props.onChange({ billType: value as BillType, page: 1 })
          }
        />
      ) : null}
      <BillSelect
        label={t('Organization')}
        value={String(props.filters.organizationId ?? 'all')}
        items={organizations}
        onValueChange={(value) =>
          props.onChange({
            organizationId: value === 'all' ? undefined : Number(value),
            page: 1,
          })
        }
      />
      <BillSelect
        label={t('Model')}
        value={props.filters.modelName ?? 'all'}
        items={models}
        onValueChange={(value) =>
          props.onChange({
            modelName: value === 'all' ? undefined : value,
            page: 1,
          })
        }
      />
      <BillSelect
        label={t('Channel')}
        value={String(props.filters.channelId ?? 'all')}
        items={channels}
        onValueChange={(value) =>
          props.onChange({
            channelId: value === 'all' ? undefined : Number(value),
            page: 1,
          })
        }
      />
      <form
        className='flex min-w-0 gap-2 md:col-span-2 xl:col-span-1'
        onSubmit={(event) => {
          event.preventDefault()
          submitSearch()
        }}
      >
        <InputGroup>
          <InputGroupAddon>
            <HugeiconsIcon icon={SearchIcon} strokeWidth={2} />
          </InputGroupAddon>
          <InputGroupInput
            value={requestId}
            onChange={(event) => setRequestId(event.target.value)}
            placeholder={t('Search local or upstream Request ID')}
            aria-label={t('Search local or upstream Request ID')}
          />
        </InputGroup>
        <Button type='submit' variant='outline'>
          {t('Search')}
        </Button>
      </form>
    </div>
  )
}

export function BillPeriodControls(props: {
  filters: BillFilters
  onChange: (patch: Partial<BillFilters>) => void
}) {
  const { t } = useTranslation()
  const startValue = dayjs
    .unix(props.filters.startTimestamp)
    .format('YYYY-MM-DD')
  const endValue = dayjs
    .unix(props.filters.endTimestamp)
    .subtract(1, 'second')
    .format('YYYY-MM-DD')
  const currentMonth = dayjs()
  const isThisMonth =
    startValue === currentMonth.startOf('month').format('YYYY-MM-DD') &&
    endValue === currentMonth.endOf('month').format('YYYY-MM-DD')
  const previousMonth = currentMonth.subtract(1, 'month')
  const latestEndDate = dayjs(startValue).add(91, 'day').format('YYYY-MM-DD')
  const isPreviousMonth =
    startValue === previousMonth.startOf('month').format('YYYY-MM-DD') &&
    endValue === previousMonth.endOf('month').format('YYYY-MM-DD')

  const setMonth = (month: typeof currentMonth) => {
    props.onChange({
      startTimestamp: month.startOf('month').unix(),
      endTimestamp: month.add(1, 'month').startOf('month').unix(),
      page: 1,
    })
  }

  return (
    <div className='flex flex-wrap items-center gap-2'>
      <div className='border-border flex rounded-lg border p-0.5'>
        <Button
          type='button'
          variant={isThisMonth ? 'secondary' : 'ghost'}
          size='sm'
          aria-pressed={isThisMonth}
          onClick={() => setMonth(currentMonth)}
        >
          {t('This month')}
        </Button>
        <Button
          type='button'
          variant={isPreviousMonth ? 'secondary' : 'ghost'}
          size='sm'
          aria-pressed={isPreviousMonth}
          onClick={() => setMonth(previousMonth)}
        >
          {t('Last month')}
        </Button>
        <Button
          type='button'
          variant={!isThisMonth && !isPreviousMonth ? 'secondary' : 'ghost'}
          size='sm'
          aria-pressed={!isThisMonth && !isPreviousMonth}
        >
          {t('Custom')}
        </Button>
      </div>
      <div className='border-input flex items-center gap-1.5 rounded-lg border px-2'>
        <HugeiconsIcon
          icon={Calendar03Icon}
          strokeWidth={2}
          className='text-muted-foreground'
          aria-hidden='true'
        />
        <Input
          type='date'
          value={startValue}
          max={endValue}
          className='h-8 w-32 border-0 px-1 shadow-none focus-visible:ring-0'
          aria-label={t('Start date')}
          onChange={(event) => {
            if (!event.target.value) return
            const nextStart = dayjs(event.target.value).startOf('day')
            props.onChange({
              startTimestamp: nextStart.unix(),
              endTimestamp:
                nextStart.unix() >= props.filters.endTimestamp
                  ? nextStart.add(1, 'day').unix()
                  : props.filters.endTimestamp,
              page: 1,
            })
          }}
        />
        <span className='text-muted-foreground'>—</span>
        <Input
          type='date'
          value={endValue}
          min={startValue}
          max={latestEndDate}
          className='h-8 w-32 border-0 px-1 shadow-none focus-visible:ring-0'
          aria-label={t('End date')}
          onChange={(event) => {
            if (!event.target.value) return
            props.onChange({
              endTimestamp: dayjs(event.target.value)
                .add(1, 'day')
                .startOf('day')
                .unix(),
              page: 1,
            })
          }}
        />
      </div>
    </div>
  )
}
