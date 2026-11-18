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
import { getRouteApi, Link } from '@tanstack/react-router'
import { lazy, Suspense, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import {
  StaticDataTable,
  type StaticDataTableColumn,
} from '@/components/data-table/static/static-data-table'
import { EmptyState } from '@/components/empty-state'
import { SectionPageLayout } from '@/components/layout'
import { StatusBadge } from '@/components/status-badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import {
  LOG_TYPE_ENUM,
  MJ_STATUS_MAPPINGS,
  TASK_STATUS_MAPPINGS,
} from '@/features/usage-logs/constants'
import { toIntlLocale } from '@/i18n/languages'
import { formatQuotaWithCurrency } from '@/lib/currency'
import { formatNumber } from '@/lib/format'
import { useAuthStore } from '@/stores/auth-store'

import { ResellerQueryStatus, ResellerTimestamp } from '../shared'
import {
  useObservationQuery,
  type AuditRow,
  type ObservationRows,
  type TaskRow,
  type UsageRow,
} from './api'
import { ObservationFilters } from './filters'
import {
  observationDates,
  observationTitle,
  type ObservationSearch,
  type ObservationView,
} from './routes'

const route = getRouteApi('/_authenticated/reseller/observe/$view')
const Analytics = lazy(() => import('./analytics'))

type Filters = ObservationSearch & { from: number; until: number }

export function ObservationPage() {
  const { t } = useTranslation()
  const { view: routeView } = route.useParams()
  const view = routeView as ObservationView
  const search = route.useSearch()
  const navigate = route.useNavigate()
  const viewer = useAuthStore((state) => state.auth.user?.id)
  const [defaults] = useState(observationDates)
  const [filterReset, setFilterReset] = useState(0)
  const kind =
    view === 'tasks' && search.kind === 'drawing' ? 'drawing' : undefined
  const filters: Filters = {
    ...search,
    from: search.from ?? defaults.from,
    until: search.until ?? defaults.until,
  }
  if (view === 'tasks') filters.kind = kind
  if (
    view === 'usage' &&
    (filters.kind === 'drawing' || filters.kind === 'async')
  ) {
    filters.kind = undefined
  }
  const key = JSON.stringify([viewer, view, filters, filterReset])
  const customers = useObservationQuery('customers')
  const setFilters = (next: ObservationSearch) =>
    void navigate({ search: next })
  const reset = () => {
    setFilterReset((current) => current + 1)
    setFilters({ kind })
  }
  return (
    <SectionPageLayout>
      <SectionPageLayout.Title>
        {observationTitle(view, t)}
      </SectionPageLayout.Title>
      <SectionPageLayout.Content>
        <div className='space-y-5'>
          <p className='text-muted-foreground text-sm'>{t('My customers')}</p>
          <ResellerQueryStatus
            pending={customers.isPending}
            error={customers.isError}
            retry={() => void customers.refetch()}
          />
          {!customers.isError && customers.data?.length === 0 && (
            <EmptyState
              title={t('No customers yet')}
              action={
                <Button
                  nativeButton={false}
                  render={<Link to='/reseller/customers' />}
                >
                  {t('Customer management')}
                </Button>
              }
            />
          )}
          {!customers.isError && !!customers.data?.length && (
            <>
              {view === 'tasks' && (
                <Tabs
                  value={kind ?? 'async'}
                  onValueChange={(value) => {
                    setFilters({
                      ...search,
                      kind: value === 'drawing' ? 'drawing' : undefined,
                    })
                  }}
                >
                  <TabsList aria-label={t('Task Logs')}>
                    <TabsTrigger value='async'>{t('Task Logs')}</TabsTrigger>
                    <TabsTrigger value='drawing'>
                      {t('Drawing Logs')}
                    </TabsTrigger>
                  </TabsList>
                </Tabs>
              )}
              <ObservationFilters
                key={`filters:${key}`}
                customers={customers.data}
                view={view}
                filters={filters}
                onApply={setFilters}
                onReset={reset}
              />
              {view === 'overview' && <Overview key={key} filters={filters} />}
              {view === 'analytics' && (
                <Suspense fallback={<p role='status'>{t('Loading...')}</p>}>
                  <Analytics key={key} filters={filters} onReset={reset} />
                </Suspense>
              )}
              {view === 'usage' && (
                <Usage key={key} filters={filters} onReset={reset} />
              )}
              {view === 'audit' && (
                <Audit key={key} filters={filters} onReset={reset} />
              )}
              {view === 'tasks' && (
                <Tasks key={key} filters={filters} onReset={reset} />
              )}
            </>
          )}
        </div>
      </SectionPageLayout.Content>
    </SectionPageLayout>
  )
}

function Overview(props: { filters: Filters }) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const query = useObservationQuery('overview', props.filters)
  const data = query.data
  if (!data || query.isError) {
    return (
      <ResellerQueryStatus
        pending={query.isPending}
        error={query.isError}
        retry={() => void query.refetch()}
      />
    )
  }
  const search = {
    from: props.filters.from,
    until: props.filters.until,
    customer_id: props.filters.customer_id,
  }
  const customerDestination = props.filters.customer_id ? (
    <Link
      to='/reseller/customers/$id'
      params={{ id: String(props.filters.customer_id) }}
    />
  ) : (
    <Link to='/reseller/customers' />
  )
  const cards = [
    { label: t('Customers'), value: data.customers, link: customerDestination },
    {
      label: t('Awaiting enablement'),
      value: data.awaiting,
      link: props.filters.customer_id ? (
        customerDestination
      ) : (
        <Link
          to='/reseller/customers'
          search={{ service_status: 'awaiting_enablement' }}
        />
      ),
    },
    {
      label: t('Active customers'),
      value: data.active,
      link: (
        <Link
          to='/reseller/observe/$view'
          params={{ view: 'usage' }}
          search={{ ...search, kind: 'consumption' }}
        />
      ),
    },
    {
      label: t('Recorded requests'),
      value: data.requests,
      link: (
        <Link
          to='/reseller/observe/$view'
          params={{ view: 'usage' }}
          search={search}
        />
      ),
    },
    {
      label: t('Recorded failures'),
      value: data.failures,
      link: (
        <Link
          to='/reseller/observe/$view'
          params={{ view: 'usage' }}
          search={{ ...search, kind: 'failure' }}
        />
      ),
    },
  ]
  return (
    <>
      <div className='grid gap-3 sm:grid-cols-2 xl:grid-cols-5'>
        {cards.map((card) => (
          <Card key={card.label}>
            <CardHeader>
              <CardTitle>{card.label}</CardTitle>
            </CardHeader>
            <CardContent>
              <Button
                nativeButton={false}
                variant='link'
                className='h-auto px-0 text-xl tabular-nums'
                aria-label={card.label}
                render={card.link}
              >
                {formatNumber(card.value, locale)}
              </Button>
            </CardContent>
          </Card>
        ))}
      </div>
      <p className='text-muted-foreground text-sm'>
        {t('Counts reflect retained usage logs.')}
      </p>
    </>
  )
}

type LogProps = { filters: Filters; onReset: () => void }

function LogTable<T>(props: {
  data?: ObservationRows<T>
  columns: StaticDataTableColumn<T>[]
  pending: boolean
  error: boolean
  retry: () => void
  onReset: () => void
  onPrevious?: () => void
  onNext: () => void
}) {
  const { t } = useTranslation()
  return (
    <>
      <ResellerQueryStatus
        pending={props.pending}
        error={props.error}
        retry={props.retry}
      />
      {props.data && !props.error && (
        <>
          {props.data.items.length > 0 ? (
            <StaticDataTable columns={props.columns} data={props.data.items} />
          ) : (
            <EmptyState
              title={t('No matching customer records')}
              action={
                <Button variant='outline' onClick={props.onReset}>
                  {t('Reset')}
                </Button>
              }
            />
          )}
          {(props.onPrevious || props.data.has_more) && (
            <div className='flex justify-end gap-2'>
              <Button
                variant='outline'
                disabled={!props.onPrevious}
                onClick={props.onPrevious}
              >
                {t('Previous page')}
              </Button>
              <Button
                variant='outline'
                disabled={!props.data.has_more}
                onClick={props.onNext}
              >
                {t('Next page')}
              </Button>
            </div>
          )}
        </>
      )}
    </>
  )
}

function Usage(props: LogProps) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const [page, setPage] = useState(1)
  const query = useObservationQuery('usage', { ...props.filters, page })
  const columns: StaticDataTableColumn<UsageRow>[] = [
    {
      id: 'time',
      header: t('Time'),
      cell: (row) => <ResellerTimestamp value={row.created_at} />,
    },
    {
      id: 'customer',
      header: t('Customer'),
      cell: (row) => row.username || row.user_id,
    },
    { id: 'model', header: t('Model'), cell: (row) => row.model_name || '—' },
    {
      id: 'result',
      header: t('Result'),
      cell: (row) =>
        row.type === LOG_TYPE_ENUM.CONSUME ? (
          <StatusBadge variant='green' label={t('Consumption')} />
        ) : (
          <StatusBadge variant='red' label={t('Failed')} />
        ),
    },
    {
      id: 'tokens',
      header: t('Tokens'),
      cell: (row) =>
        formatNumber(row.prompt_tokens + row.completion_tokens, locale),
    },
    {
      id: 'latency',
      header: t('Duration'),
      cell: (row) =>
        t('{{seconds}} s', { seconds: formatNumber(row.use_time, locale) }),
    },
    {
      id: 'request',
      header: t('Request ID'),
      cell: (row) => row.request_id || '—',
    },
    {
      id: 'billing',
      header: t('Customer billing'),
      cell: (row) =>
        row.request_id && (
          <Link
            className='text-primary underline'
            to='/reseller/billing'
            search={{
              customer: String(row.user_id),
              request_id: row.request_id,
            }}
          >
            {t('View customer bill')}
          </Link>
        ),
    },
  ]
  return (
    <LogTable
      data={query.data}
      columns={columns}
      pending={query.isPending}
      error={query.isError}
      retry={() => void query.refetch()}
      onReset={props.onReset}
      onPrevious={page > 1 ? () => setPage(page - 1) : undefined}
      onNext={() => setPage(page + 1)}
    />
  )
}

function Audit(props: LogProps) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const [history, setHistory] = useState<
    { after_time: number; after_event: string }[]
  >([])
  const query = useObservationQuery('audit', {
    ...props.filters,
    ...history.at(-1),
  })
  const actionNames: Record<string, string> = {
    'reseller.customer.create': t('Create customer'),
    'reseller.enabled.change': t('Change customer status'),
    'reseller.quota.adjust': t('Adjust quota'),
    'reseller.config.save': t('Change customer model settings'),
  }
  const details = (row: AuditRow): ReactNode => {
    if (row.enabled !== undefined) {
      return row.enabled ? t('Enabled') : t('Disabled')
    }
    if (row.quota !== undefined && row.mode) {
      return (
        (row.mode === 'subtract' ? '−' : '+') +
        formatQuotaWithCurrency(row.quota, { locale })
      )
    }
    if (row.revision !== undefined) {
      return t('Revision {{revision}}', { revision: row.revision })
    }
    return '—'
  }
  return (
    <LogTable
      data={query.data}
      columns={[
        {
          id: 'time',
          header: t('Time'),
          cell: (row) => <ResellerTimestamp value={row.created_at} />,
        },
        { id: 'operator', header: t('Operator'), cell: (row) => row.operator },
        { id: 'customer', header: t('Customer'), cell: (row) => row.username },
        {
          id: 'action',
          header: t('Operation'),
          cell: (row) => actionNames[row.action] ?? '—',
        },
        { id: 'summary', header: t('Details'), cell: details },
      ]}
      pending={query.isPending}
      error={query.isError}
      retry={() => void query.refetch()}
      onReset={props.onReset}
      onPrevious={
        history.length > 0
          ? () => setHistory((current) => current.slice(0, -1))
          : undefined
      }
      onNext={() => {
        const last = query.data?.items.at(-1)
        if (last) {
          setHistory((current) => [
            ...current,
            { after_time: last.created_at, after_event: last.event_id },
          ])
        }
      }}
    />
  )
}

function Tasks(props: LogProps) {
  const { t } = useTranslation()
  const [page, setPage] = useState(1)
  const query = useObservationQuery('tasks', { ...props.filters, page })
  const statuses =
    props.filters.kind === 'drawing' ? MJ_STATUS_MAPPINGS : TASK_STATUS_MAPPINGS
  const columns: StaticDataTableColumn<TaskRow>[] = [
    { id: 'task', header: t('Task ID'), cell: (row) => row.task_id },
    {
      id: 'customer',
      header: t('Customer'),
      cell: (row) => row.username || row.user_id,
    },
    { id: 'type', header: t('Type'), cell: (row) => row.action || '—' },
    {
      id: 'status',
      header: t('Status'),
      cell: (row) => {
        const status = Object.hasOwn(statuses, row.status)
          ? statuses[row.status]
          : undefined
        return (
          <StatusBadge
            label={t(status?.label ?? 'Unknown')}
            variant={status?.variant ?? 'neutral'}
          />
        )
      },
    },
    {
      id: 'progress',
      header: t('Progress'),
      cell: (row) => row.progress || '—',
    },
    {
      id: 'submit',
      header: t('Submit Time'),
      cell: (row) => <ResellerTimestamp value={row.submit_time} />,
    },
    {
      id: 'start',
      header: t('Start Time'),
      cell: (row) =>
        row.start_time ? <ResellerTimestamp value={row.start_time} /> : '—',
    },
    {
      id: 'finish',
      header: t('Finish Time'),
      cell: (row) =>
        row.finish_time ? <ResellerTimestamp value={row.finish_time} /> : '—',
    },
  ]
  return (
    <LogTable
      data={query.data}
      columns={columns}
      pending={query.isPending}
      error={query.isError}
      retry={() => void query.refetch()}
      onReset={props.onReset}
      onPrevious={page > 1 ? () => setPage(page - 1) : undefined}
      onNext={() => setPage(page + 1)}
    />
  )
}
