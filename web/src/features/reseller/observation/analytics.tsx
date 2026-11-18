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
import { CartesianGrid, Line, LineChart, XAxis, YAxis } from 'recharts'

import { StaticDataTable } from '@/components/data-table/static/static-data-table'
import { EmptyState } from '@/components/empty-state'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import {
  ChartContainer,
  ChartTooltip,
  ChartTooltipContent,
} from '@/components/ui/chart'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber } from '@/lib/format'

import { ResellerQueryStatus } from '../shared'
import { useObservationQuery } from './api'
import { observationDay, type ObservationSearch } from './routes'

export default function Analytics(props: {
  filters: ObservationSearch & { from: number; until: number }
  onReset: () => void
}) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const [metric, setMetric] = useState<'requests' | 'tokens'>('requests')
  const query = useObservationQuery('analytics', props.filters)
  const date = new Intl.DateTimeFormat(locale, {
    timeZone: 'UTC',
    month: 'short',
    day: 'numeric',
  })
  const daily = new Map<number, { requests: number; tokens: number }>()
  const models = new Map<string, { requests: number; tokens: number }>()
  for (const point of query.data ?? []) {
    const day = daily.get(point.day_start) ?? { requests: 0, tokens: 0 }
    daily.set(point.day_start, {
      requests: day.requests + point.requests,
      tokens: day.tokens + point.tokens,
    })
    const model = models.get(point.model) ?? { requests: 0, tokens: 0 }
    models.set(point.model, {
      requests: model.requests + point.requests,
      tokens: model.tokens + point.tokens,
    })
  }
  const days = []
  if (daily.size > 0) {
    for (
      let day =
        Math.floor(props.filters.from / observationDay) * observationDay;
      day < props.filters.until;
      day += observationDay
    ) {
      days.push({ day, ...(daily.get(day) ?? { requests: 0, tokens: 0 }) })
    }
  }
  const modelRows = Array.from(models, ([model, counts]) => ({
    model,
    ...counts,
  })).sort((a, b) => b[metric] - a[metric] || a.model.localeCompare(b.model))
  const config = {
    requests: { label: t('Recorded requests'), color: 'var(--chart-1)' },
    tokens: { label: t('Tokens'), color: 'var(--chart-2)' },
  }

  return (
    <>
      <ResellerQueryStatus
        pending={query.isPending}
        error={query.isError}
        retry={() => void query.refetch()}
      />
      {!query.isError && query.data?.length === 0 && (
        <EmptyState
          title={t('No matching customer records')}
          action={
            <Button variant='outline' onClick={props.onReset}>
              {t('Reset')}
            </Button>
          }
        />
      )}
      {!query.isError && days.length > 0 && (
        <>
          <p className='text-muted-foreground text-sm'>
            {t('Counts reflect retained usage logs.')}
          </p>
          <Card>
            <CardHeader>
              <CardTitle role='heading' aria-level={3}>
                {t('Daily trend')} (UTC)
              </CardTitle>
              <Tabs
                value={metric}
                onValueChange={(value) =>
                  setMetric(value === 'tokens' ? 'tokens' : 'requests')
                }
              >
                <TabsList aria-label={t('Daily trend')}>
                  <TabsTrigger value='requests'>
                    {t('Recorded requests')}
                  </TabsTrigger>
                  <TabsTrigger value='tokens'>{t('Tokens')}</TabsTrigger>
                </TabsList>
              </Tabs>
            </CardHeader>
            <CardContent>
              <ChartContainer config={config} className='h-64 w-full'>
                <LineChart accessibilityLayer data={days}>
                  <CartesianGrid vertical={false} />
                  <XAxis
                    dataKey='day'
                    tickFormatter={(value: number) => date.format(value * 1000)}
                  />
                  <YAxis
                    allowDecimals={false}
                    tickFormatter={(value: number) =>
                      formatNumber(value, locale)
                    }
                  />
                  <ChartTooltip
                    content={
                      <ChartTooltipContent
                        labelFormatter={(value) =>
                          date.format(Number(value) * 1000)
                        }
                      />
                    }
                  />
                  <Line
                    dataKey={metric}
                    stroke={`var(--color-${metric})`}
                    isAnimationActive={false}
                  />
                </LineChart>
              </ChartContainer>
            </CardContent>
          </Card>
          <Card>
            <CardHeader>
              <CardTitle role='heading' aria-level={3}>
                {t('Model distribution')}
              </CardTitle>
            </CardHeader>
            <CardContent>
              <StaticDataTable
                data={modelRows}
                getRowKey={(row) => row.model}
                columns={[
                  {
                    id: 'model',
                    header: t('Model'),
                    cell: (row) => row.model || t('Unknown'),
                  },
                  {
                    id: 'requests',
                    header: t('Recorded requests'),
                    cell: (row) => formatNumber(row.requests, locale),
                  },
                  {
                    id: 'tokens',
                    header: t('Tokens'),
                    cell: (row) => formatNumber(row.tokens, locale),
                  },
                ]}
              />
            </CardContent>
          </Card>
        </>
      )}
    </>
  )
}
