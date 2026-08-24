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
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import type { AxiosError } from 'axios'
import { History, Plus, Save, Trash2 } from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Field, FieldError } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'

import { formatTimestampInBeijingTime } from './beijing-time'
import {
  getAdminOrganizationDiscount,
  getAdminOrganizationDiscountChannelOptions,
  getAdminOrganizationDiscountHistory,
  organizationDiscountKeys,
  updateAdminOrganizationDiscount,
} from './discount-api'
import {
  hasInvalidDiscountRows,
  isValidDiscountRatio,
  nextDiscountRowKey,
  rowsFromDiscounts,
  rowsSignature,
  rowsToPayload,
  type DiscountRow,
} from './discount-rows'
import type { OrganizationDiscountHistoryItem } from './discount-types'

const HISTORY_PAGE_SIZE = 5

function DiscountPanel({
  title,
  description,
  actions,
  children,
}: {
  title: string
  description?: string
  actions?: React.ReactNode
  children: React.ReactNode
}) {
  return (
    <section className='bg-background rounded-lg border'>
      <div className='flex flex-col gap-3 border-b p-4 sm:flex-row sm:items-center sm:justify-between'>
        <div className='min-w-0'>
          <h2 className='truncate text-base font-medium'>{title}</h2>
          {description ? (
            <p className='text-muted-foreground mt-1 text-sm'>{description}</p>
          ) : null}
        </div>
        {actions ? <div className='flex shrink-0 gap-2'>{actions}</div> : null}
      </div>
      <div className='p-4'>{children}</div>
    </section>
  )
}

function HistoryContent({
  isLoading,
  items,
  page,
  pageCount,
  channels,
  onPageChange,
}: {
  isLoading: boolean
  items: OrganizationDiscountHistoryItem[]
  page: number
  pageCount: number
  channels: Map<number, string>
  onPageChange: (page: number) => void
}) {
  const { t } = useTranslation()
  if (isLoading) {
    return (
      <div className='space-y-2'>
        <p className='text-muted-foreground py-6 text-center text-sm'>
          {t('Loading...')}
        </p>
      </div>
    )
  }
  if (items.length === 0) {
    return (
      <div className='space-y-2'>
        <p className='text-muted-foreground py-6 text-center text-sm'>
          {t('No history yet')}
        </p>
      </div>
    )
  }
  return (
    <div className='space-y-2'>
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>{t('Saved at')}</TableHead>
            <TableHead>{t('Operator')}</TableHead>
            <TableHead>{t('Changes')}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {items.map((item) => (
            <TableRow key={item.snapshot_id}>
              <TableCell className='whitespace-nowrap'>
                {formatTimestampInBeijingTime(item.created_at)}
              </TableCell>
              <TableCell>
                {item.created_by_name ||
                  (item.created_by ? `#${item.created_by}` : t('Unknown'))}
              </TableCell>
              <TableCell>
                {item.changes.length === 0 ? (
                  <span className='text-muted-foreground'>
                    {t('No changes')}
                  </span>
                ) : (
                  <div className='flex flex-wrap gap-1.5'>
                    {item.changes.map((change) => (
                      <Badge key={change.channel_id} variant='outline'>
                        {channels.get(change.channel_id) ??
                          `CH ${change.channel_id}`}
                        : {change.old_ratio || '1'} →{' '}
                        {change.new_ratio || t('removed')}
                      </Badge>
                    ))}
                  </div>
                )}
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
      <div className='flex items-center justify-end gap-3 pt-3'>
        <div className='text-muted-foreground text-sm'>
          {t('Page')} {page} / {pageCount}
        </div>
        <Button
          variant='outline'
          size='sm'
          disabled={page <= 1}
          onClick={() => onPageChange(page - 1)}
        >
          {t('Previous')}
        </Button>
        <Button
          variant='outline'
          size='sm'
          disabled={page >= pageCount}
          onClick={() => onPageChange(page + 1)}
        >
          {t('Next')}
        </Button>
      </div>
    </div>
  )
}

/**
 * 组织渠道折扣面板（仅 Root）：每渠道独立折扣，未配置渠道按原价计费。
 * 批量编辑后一次保存、原子生效；历史为不可变快照，只读展示派生变更。
 */
export function OrganizationDiscountPanel({
  organizationId,
}: {
  organizationId: number
}) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()

  const discountQuery = useQuery({
    queryKey: organizationDiscountKeys.current(organizationId),
    queryFn: () => getAdminOrganizationDiscount(organizationId),
  })

  // 渠道选项来自折扣专用 Root 接口（全局渠道表），不依赖消费日志聚合，
  // 新组织与新渠道都能直接配置折扣。
  const channelOptionsQuery = useQuery({
    queryKey: [
      'admin',
      'organizations',
      organizationId,
      'discount',
      'channel-options',
    ],
    queryFn: () => getAdminOrganizationDiscountChannelOptions(organizationId),
  })

  const [draftRows, setDraftRows] = useState<DiscountRow[] | null>(null)
  const [historyOpen, setHistoryOpen] = useState(false)
  const [historyPage, setHistoryPage] = useState(1)

  const discount = discountQuery.data?.data
  const snapshotId = discount?.snapshot_id
  const serverRows = useMemo(
    () => rowsFromDiscounts(discount?.channel_discounts),
    [discount?.channel_discounts]
  )

  // 本地编辑区（draftRows）覆盖服务器行；快照 ID 变化（保存成功或冲突刷新）
  // 时丢弃草稿、回到最新服务器状态，避免窗口聚焦重取清空未保存编辑。
  useEffect(() => {
    setDraftRows(null)
  }, [snapshotId])

  const rows = draftRows ?? serverRows

  const historyQuery = useQuery({
    queryKey: organizationDiscountKeys.history(
      organizationId,
      historyPage,
      HISTORY_PAGE_SIZE
    ),
    queryFn: () =>
      getAdminOrganizationDiscountHistory(
        organizationId,
        historyPage,
        HISTORY_PAGE_SIZE
      ),
    enabled: historyOpen,
  })

  const channels = useMemo(() => {
    const map = new Map<number, string>()
    for (const option of channelOptionsQuery.data?.data ?? []) {
      map.set(option.id, option.name || `#${option.id}`)
    }
    return map
  }, [channelOptionsQuery.data])

  const dirty =
    draftRows !== null &&
    rowsSignature(rows) !== rowsSignature(serverRows) &&
    !discountQuery.isLoading

  const saveMutation = useMutation({
    mutationFn: () =>
      updateAdminOrganizationDiscount(organizationId, {
        expected_snapshot_id: discount?.snapshot_id ?? 0,
        channel_discounts: rowsToPayload(rows),
      }),
    onSuccess: (res) => {
      if (!res.success) {
        toast.error(res.message || t('Save failed'))
        return
      }
      toast.success(t('Discounts saved'))
      // 前缀失效同时覆盖当前折扣与历史查询。
      void queryClient.invalidateQueries({
        queryKey: ['admin', 'organizations', organizationId, 'discount'],
      })
    },
    onError: (error: AxiosError<{ message?: string }>) => {
      if (error.response?.status === 409) {
        toast.error(
          t(
            'The discount configuration was changed by someone else. Please review the latest configuration and try again.'
          )
        )
        void queryClient.invalidateQueries({
          queryKey: organizationDiscountKeys.current(organizationId),
        })
        return
      }
      toast.error(error.response?.data?.message || t('Save failed'))
    },
  })

  const addRow = () => {
    setDraftRows((prev) => {
      const current = prev ?? serverRows
      // 行 key 必须全局唯一：取现有最大 key + 1，避免与服务器行（1..N）冲突。
      const key = nextDiscountRowKey(current)
      return [...current, { key, channelId: 0, ratio: '1.0' }]
    })
  }

  const updateRow = (
    key: number,
    patch: Partial<{ channelId: number; ratio: string }>
  ) => {
    setDraftRows((prev) =>
      (prev ?? serverRows).map((row) =>
        row.key === key ? { ...row, ...patch } : row
      )
    )
  }

  const removeRow = (key: number) => {
    setDraftRows((prev) =>
      (prev ?? serverRows).filter((row) => row.key !== key)
    )
  }

  const saveDisabled =
    saveMutation.isPending ||
    discountQuery.isLoading ||
    hasInvalidDiscountRows(rows)

  const history = historyQuery.data?.data
  const historyPageCount = Math.max(
    1,
    Math.ceil((history?.total ?? 0) / HISTORY_PAGE_SIZE)
  )

  return (
    <div className='space-y-4'>
      <DiscountPanel
        title={t('Channel discounts')}
        description={t(
          'Discounts are configured per channel; unconfigured channels bill at full price. Edits apply together on save.'
        )}
        actions={
          <div className='flex items-center gap-2'>
            {dirty ? (
              <Badge variant='outline' className='text-warning border-warning'>
                {t('Unsaved changes')}
              </Badge>
            ) : null}
            <Button
              onClick={() => saveMutation.mutate()}
              disabled={saveDisabled}
            >
              <Save data-icon='inline-start' />
              {saveMutation.isPending ? t('Saving...') : t('Save')}
            </Button>
          </div>
        }
      >
        <div className='space-y-2'>
          <div className='flex items-center justify-between'>
            <p className='text-muted-foreground text-xs'>
              {t('Snapshot ID')}: {discount?.snapshot_id ?? 0}
            </p>
            <Button type='button' variant='outline' size='sm' onClick={addRow}>
              <Plus className='mr-1 size-4' />
              {t('Add channel')}
            </Button>
          </div>
          {rows.length === 0 ? (
            <p className='text-muted-foreground py-6 text-center text-sm'>
              {t('No channel discounts configured.')}
            </p>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{t('Channel')}</TableHead>
                  <TableHead>{t('Discount ratio')}</TableHead>
                  <TableHead className='w-16' />
                </TableRow>
              </TableHeader>
              <TableBody>
                {rows.map((row) => {
                  const ratioInvalid = !isValidDiscountRatio(row.ratio)
                  const ratioErrorId = `discount-ratio-error-${row.key}`
                  return (
                    <TableRow key={row.key}>
                      <TableCell>
                        <NativeSelect
                          value={row.channelId || ''}
                          onChange={(e) =>
                            updateRow(row.key, {
                              channelId: Number(e.target.value),
                            })
                          }
                        >
                          <NativeSelectOption value=''>
                            {t('Select channel')}
                          </NativeSelectOption>
                          {[...channels.entries()].map(([id, name]) => (
                            <NativeSelectOption key={id} value={id}>
                              {name}
                            </NativeSelectOption>
                          ))}
                        </NativeSelect>
                      </TableCell>
                      <TableCell>
                        <Field data-invalid={ratioInvalid}>
                          <Input
                            type='number'
                            inputMode='decimal'
                            min='0.000001'
                            max='1'
                            step='0.000001'
                            value={row.ratio}
                            onChange={(e) =>
                              updateRow(row.key, { ratio: e.target.value })
                            }
                            aria-label={t('Discount ratio')}
                            aria-invalid={ratioInvalid}
                            aria-describedby={
                              ratioInvalid ? ratioErrorId : undefined
                            }
                            placeholder='0.80'
                          />
                          {ratioInvalid ? (
                            <FieldError id={ratioErrorId}>
                              {t(
                                'Enter a number greater than 0 and no greater than 1, with up to 6 decimal places.'
                              )}
                            </FieldError>
                          ) : null}
                        </Field>
                      </TableCell>
                      <TableCell>
                        <Button
                          type='button'
                          variant='ghost'
                          size='icon'
                          onClick={() => removeRow(row.key)}
                        >
                          <Trash2 className='size-4' />
                        </Button>
                      </TableCell>
                    </TableRow>
                  )
                })}
              </TableBody>
            </Table>
          )}
        </div>
      </DiscountPanel>

      <DiscountPanel
        title={t('Discount history')}
        description={t(
          'Immutable snapshots with the channel changes derived from the previous snapshot.'
        )}
        actions={
          <Button
            variant='outline'
            onClick={() => setHistoryOpen((open) => !open)}
          >
            <History className='mr-1 size-4' />
            {historyOpen ? t('Hide') : t('Show')}
          </Button>
        }
      >
        {historyOpen ? (
          <HistoryContent
            isLoading={historyQuery.isLoading}
            items={history?.items ?? []}
            page={historyPage}
            pageCount={historyPageCount}
            channels={channels}
            onPageChange={setHistoryPage}
          />
        ) : (
          <p className='text-muted-foreground py-2 text-center text-sm'>
            {t('Show the immutable discount history.')}
          </p>
        )}
      </DiscountPanel>
    </div>
  )
}
