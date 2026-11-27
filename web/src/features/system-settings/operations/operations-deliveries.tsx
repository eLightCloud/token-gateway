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
import {
  keepPreviousData,
  useMutation,
  useQuery,
  useQueryClient,
} from '@tanstack/react-query'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { ConfirmDialog } from '@/components/confirm-dialog'
import {
  DataTablePagination,
  StaticDataTable,
  useDataTable,
} from '@/components/data-table'
import { Dialog } from '@/components/dialog'
import { ErrorState } from '@/components/error-state'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import {
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { handleServerError } from '@/lib/handle-server-error'

import {
  type OperationsDelivery,
  getOperationsDeliveries,
  operationsDeliveriesQueryKey,
  retryOperationsDelivery,
} from './api'

const EMPTY_DELIVERIES: OperationsDelivery[] = []

export function OperationsDeliveries() {
  const { t } = useTranslation()
  const client = useQueryClient()
  const [pagination, setPagination] = useState({ pageIndex: 0, pageSize: 10 })
  const query = useQuery({
    queryKey: [...operationsDeliveriesQueryKey, pagination],
    queryFn: ({ signal }) =>
      getOperationsDeliveries(
        pagination.pageIndex + 1,
        pagination.pageSize,
        signal
      ),
    placeholderData: keepPreviousData,
    staleTime: 30_000,
    refetchOnWindowFocus: false,
    meta: { errorToast: false },
  })
  const deliveries = query.data?.items ?? EMPTY_DELIVERIES
  const { table } = useDataTable({
    data: deliveries,
    columns: [],
    totalCount: query.data?.total ?? 0,
    manualPagination: true,
    columnVisibilityStorageKey: false,
    columnSizingStorageKey: false,
    pagination,
    onPaginationChange: setPagination,
    ensurePageInRange: (pageCount) => {
      const lastPageIndex = Math.max(0, pageCount - 1)
      if (
        query.isSuccess &&
        !query.isPlaceholderData &&
        pagination.pageIndex > lastPageIndex
      ) {
        setPagination((previous) => ({ ...previous, pageIndex: lastPageIndex }))
      }
    },
  })
  const [selected, setSelected] = useState<OperationsDelivery | null>(null)
  const [retry, setRetry] = useState<OperationsDelivery | null>(null)
  const resend = useMutation({
    mutationFn: retryOperationsDelivery,
    onSuccess: async () => {
      setRetry(null)
      await client.invalidateQueries({ queryKey: operationsDeliveriesQueryKey })
    },
    onError: (error) => handleServerError(error),
  })
  const statusLabels: Record<string, string> = {
    pending: t('Notification status: pending'),
    preview: t('Notification status: preview'),
    sending: t('Notification status: sending'),
    accepted: t('Notification status: accepted'),
    failed: t('Notification status: failed'),
    unknown: t('Notification status: unknown'),
    skipped: t('Notification status: skipped'),
  }
  const preview = selected
    ? new DOMParser().parseFromString(
        selected.body.replaceAll(/<\/(p|h2)>/g, '\n'),
        'text/html'
      ).body.textContent
    : ''
  return (
    <div className='space-y-3'>
      <div className='flex items-center justify-between gap-3'>
        <h4 className='text-sm font-medium'>
          {t('Recent notification deliveries')}
        </h4>
        <Button
          variant='outline'
          size='sm'
          disabled={query.isFetching}
          onClick={() => void query.refetch()}
        >
          {t('Refresh')}
        </Button>
      </div>
      <div aria-busy={query.isFetching} className='space-y-3'>
        {(query.isPending || query.isPlaceholderData) && (
          <Skeleton className='h-24 w-full' />
        )}
        {query.isError && (
          <ErrorState
            title={t('Failed to load')}
            onRetry={() => void query.refetch()}
          />
        )}
        {query.isSuccess && !query.isPlaceholderData && (
          <StaticDataTable tableClassName='min-w-[600px]'>
            <TableHeader>
              <TableRow>
                <TableHead>{t('Date')}</TableHead>
                <TableHead>{t('Recipient')}</TableHead>
                <TableHead>{t('Status')}</TableHead>
                <TableHead>{t('Actions')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {deliveries.length === 0 && (
                <TableRow>
                  <TableCell colSpan={4}>
                    {t('No notification deliveries yet.')}
                  </TableCell>
                </TableRow>
              )}
              {deliveries.map((delivery) => (
                <TableRow key={delivery.key}>
                  <TableCell>{delivery.day}</TableCell>
                  <TableCell className='max-w-56 truncate'>
                    {delivery.recipient || t('Unavailable')}
                  </TableCell>
                  <TableCell>
                    {statusLabels[delivery.status] ?? t('Unavailable')}
                    {delivery.reason && (
                      <p className='text-muted-foreground text-xs'>
                        {delivery.reason}
                      </p>
                    )}
                  </TableCell>
                  <TableCell>
                    <div className='flex gap-2'>
                      <Button
                        variant='outline'
                        size='sm'
                        onClick={() => setSelected(delivery)}
                      >
                        {t('Preview')}
                      </Button>
                      {delivery.kind !== 'emergency' &&
                        ['failed', 'unknown', 'preview'].includes(
                          delivery.status
                        ) && (
                          <Button
                            variant='outline'
                            size='sm'
                            onClick={() => setRetry(delivery)}
                            disabled={resend.isPending}
                          >
                            {t('Retry')}
                          </Button>
                        )}
                    </div>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </StaticDataTable>
        )}
        {!!query.data?.total && <DataTablePagination table={table} compact />}
      </div>
      <Dialog
        open={selected !== null}
        onOpenChange={(open) => {
          if (!open) setSelected(null)
        }}
        title={selected?.subject ?? t('Preview')}
      >
        <pre className='text-sm break-words whitespace-pre-wrap'>{preview}</pre>
      </Dialog>
      <ConfirmDialog
        open={retry !== null}
        onOpenChange={(open) => {
          if (!open) setRetry(null)
        }}
        title={t('Retry notification')}
        desc={t(
          'An unknown delivery may already have reached the recipient. Retry only after checking. Preview mode never sends daily email.'
        )}
        isLoading={resend.isPending}
        handleConfirm={() => {
          if (retry) resend.mutate(retry.key)
        }}
      />
    </div>
  )
}
