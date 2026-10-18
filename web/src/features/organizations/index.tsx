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
import { Link } from '@tanstack/react-router'
import {
  Activity,
  ArrowDownToLine,
  ArrowUpFromLine,
  Building2,
  CalendarClock,
  Download,
  Plus,
  RefreshCw,
  Search,
  Settings,
  Trash2,
  UserPlus,
  Users,
  Wallet,
  type LucideIcon,
} from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { ErrorState } from '@/components/error-state'
import { SectionPageLayout } from '@/components/layout'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from '@/components/ui/empty'
import { IconBadge, type IconBadgeTone } from '@/components/ui/icon-badge'
import { Input } from '@/components/ui/input'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { Separator } from '@/components/ui/separator'
import { Skeleton } from '@/components/ui/skeleton'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { searchUsers } from '@/features/users/api'
import { USER_ROLE } from '@/features/users/constants'
import type { User } from '@/features/users/types'
import { formatBillingCurrencyFromUSD } from '@/lib/currency'
import {
  formatBillingAmountFromQuota,
  formatNumber,
  formatPercent,
  formatTimestampToDate,
  stringToColor,
} from '@/lib/format'
import { ROLE } from '@/lib/roles'
import { cn } from '@/lib/utils'
import { useAuthStore } from '@/stores/auth-store'

import {
  addAdminOrganizationMember,
  addCurrentOrganizationMember,
  buildAdminOrganizationLogsExportUrl,
  buildOrganizationLogsExportUrl,
  downloadOrganizationExport,
  createAdminOrganization,
  getAdminOrganization,
  getAdminOrganizationMembers,
  getAdminOrganizations,
  getCurrentOrganizationMembers,
  getOrganizationSelf,
  organizationKeys,
  removeAdminOrganizationMember,
  removeCurrentOrganizationMember,
  updateAdminOrganization,
  updateAdminOrganizationMember,
  updateCurrentOrganization,
  updateCurrentOrganizationMember,
  previewCurrentOrganizationMemberBillingStart,
  previewAdminOrganizationMemberBillingStart,
  updateCurrentOrganizationMemberBillingStart,
  updateAdminOrganizationMemberBillingStart,
} from './api'
import {
  formatTimestampInBeijingTime,
  unixTimestampToBeijingDateInput,
} from './beijing-time'
import { canViewOrganizationBillingChannels } from './billing-visibility'
import { OrganizationDiscountPanel } from './discount-panel'
import { FilterDraftNotice } from './filter-draft-notice'
import { OrganizationInvoicePanel } from './invoice'
import { OrganizationRecalculateButton } from './organization-recalculate-button'
import { OrganizationUsageBuildNotice } from './organization-usage-build-notice'
import {
  ORGANIZATION_STATUS_DISABLED,
  ORGANIZATION_STATUS_ENABLED,
  type Organization,
  type OrganizationBillingFilterOptions,
  type OrganizationDimensionRow,
  type OrganizationBillingStartPreview,
  type OrganizationBillingStartUpdatePayload,
  type OrganizationMember,
  type OrganizationRole,
  type OrganizationStatus,
  type OrganizationSummary,
  type OrganizationTrendRow,
} from './types'
import { UsageDateRangeFields } from './usage-date-range'
import {
  buildUsageSnapshotExportUrl,
  type OrganizationUsageLogItem,
  type OrganizationUsageSnapshot,
} from './usage-snapshot-api'
import { useOrganizationUsageQuery } from './use-organization-usage-query'
import { dateToUnix, type useUsageFilters } from './use-usage-filters'

const ROLE_OPTIONS: OrganizationRole[] = ['admin', 'member']
type OrganizationDetailTab =
  | 'members'
  | 'billing'
  | 'invoice'
  | 'logs'
  | 'discount'

// 摘要骨架屏占位槽位（稳定 key，避免使用数组 index 作为 key）。
const SUMMARY_CARD_SKELETONS = [
  'requests',
  'amount',
  'prompt',
  'completion',
  'members',
] as const

function canManageMembers(role?: OrganizationRole) {
  return role === 'admin'
}

function canViewBilling(role?: OrganizationRole) {
  return role === 'admin' || role === 'member'
}

function roleLabel(role: OrganizationRole, t: (key: string) => string) {
  const labels: Record<OrganizationRole, string> = {
    admin: t('Admin'),
    member: t('Member'),
  }
  return labels[role]
}

function statusLabel(status: OrganizationStatus, t: (key: string) => string) {
  return status === ORGANIZATION_STATUS_ENABLED ? t('Active') : t('Suspended')
}

function roleBadgeVariant(role: OrganizationRole) {
  return role === 'admin' ? 'default' : 'outline'
}

function organizationDetailTabLabel(
  tab: OrganizationDetailTab,
  t: (key: string) => string
) {
  if (tab === 'members') return t('Members')
  if (tab === 'billing') return t('Billing')
  if (tab === 'invoice') return t('Invoice')
  if (tab === 'discount') return t('Discount')
  return t('Logs')
}

function orgStatusTone(status: OrganizationStatus): 'success' | 'warning' {
  return status === ORGANIZATION_STATUS_ENABLED ? 'success' : 'warning'
}

const DOT_BADGE_TONE_CLASS = {
  success: 'bg-success',
  warning: 'bg-warning',
  muted: 'bg-muted-foreground',
} as const

// 状态/成员状态统一呈现：圆点 + 文字，避免仅靠颜色表达语义。
function DotBadge({
  tone,
  children,
}: {
  tone: 'success' | 'warning' | 'muted'
  children: React.ReactNode
}) {
  return (
    <Badge variant='outline' className='gap-1.5'>
      <span
        className={cn('size-1.5 rounded-full', DOT_BADGE_TONE_CLASS[tone])}
        aria-hidden='true'
      />
      {children}
    </Badge>
  )
}

// 表内功能性占比条：既有 Share / 趋势数值的可视化呈现，非装饰性图表。
function ProportionBar({
  value,
  fillClassName = 'bg-primary',
}: {
  value: number
  fillClassName?: string
}) {
  const pct = Math.max(0, Math.min(100, Number.isFinite(value) ? value : 0))
  return (
    <div
      className='bg-muted h-1 w-full overflow-hidden rounded-full'
      role='presentation'
    >
      <div
        className={cn(
          'h-full rounded-full motion-safe:transition-all',
          fillClassName
        )}
        style={{ width: `${pct}%` }}
      />
    </div>
  )
}

function EntityAvatar({
  name,
  size = 'md',
}: {
  name: string
  size?: 'sm' | 'md'
}) {
  const label = (name || '?').trim()
  const initial = label.charAt(0).toUpperCase() || '?'
  const sizeClass = size === 'sm' ? 'size-8 text-xs' : 'size-9 text-sm'
  return (
    <span
      className={cn(
        'flex shrink-0 items-center justify-center rounded-full font-semibold text-white',
        sizeClass
      )}
      style={{ backgroundColor: stringToColor(label) }}
      aria-hidden='true'
    >
      {initial}
    </span>
  )
}

function FieldLabel({
  label,
  children,
}: {
  label: string
  children: React.ReactNode
}) {
  return (
    <label className='block space-y-1'>
      <span className='text-muted-foreground text-xs font-medium'>{label}</span>
      {children}
    </label>
  )
}

// 摘要指标卡片：对齐仪表盘视觉语言（IconBadge + tabular-nums + 主指标强调）。
function BillingStatCard({
  label,
  value,
  description,
  icon: Icon,
  iconTone,
  emphasis,
}: {
  label: string
  value: string
  description?: string
  icon: LucideIcon
  iconTone: IconBadgeTone
  emphasis?: boolean
}) {
  return (
    <div
      className={cn(
        'rounded-lg border p-4',
        emphasis && 'border-primary/30 bg-primary/[0.04]'
      )}
    >
      <div className='flex items-center gap-2'>
        <IconBadge tone={iconTone} size='sm'>
          <Icon />
        </IconBadge>
        <span className='text-muted-foreground truncate text-xs font-medium'>
          {label}
        </span>
      </div>
      <div
        className={cn(
          'mt-2 font-mono font-semibold tabular-nums',
          emphasis ? 'text-primary text-2xl' : 'text-xl'
        )}
      >
        {value}
      </div>
      {description ? (
        <div className='text-muted-foreground/70 mt-1 truncate text-[11px]'>
          {description}
        </div>
      ) : null}
    </div>
  )
}

function Panel({
  title,
  description,
  actions,
  children,
  className,
}: {
  title: string
  description?: string
  actions?: React.ReactNode
  children: React.ReactNode
  className?: string
}) {
  return (
    <section className={cn('rounded-lg border bg-background', className)}>
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

function OrgSectionTitle({ title, name }: { title: string; name: string }) {
  return (
    <SectionPageLayout.Title>
      <span className='flex min-w-0 flex-col gap-0.5'>
        <span className='truncate'>{title}</span>
        <span className='text-muted-foreground truncate text-xs font-normal sm:text-sm'>
          {name}
        </span>
      </span>
    </SectionPageLayout.Title>
  )
}

function PageHeader({
  title,
  description,
  actions,
}: {
  title: string
  description: string
  actions?: React.ReactNode
}) {
  return (
    <div className='flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between'>
      <div className='min-w-0'>
        <h1 className='truncate text-2xl font-semibold tracking-normal'>
          {title}
        </h1>
        <p className='text-muted-foreground mt-1 text-sm'>{description}</p>
      </div>
      {actions ? <div className='flex shrink-0 gap-2'>{actions}</div> : null}
    </div>
  )
}

function OrganizationEmptyState() {
  const { t } = useTranslation()
  return (
    <div className='p-4 sm:p-6'>
      <Empty className='min-h-[360px] border'>
        <EmptyHeader>
          <EmptyMedia variant='icon'>
            <Building2 />
          </EmptyMedia>
          <EmptyTitle>{t('No organization')}</EmptyTitle>
          <EmptyDescription>
            {t('You are not a member of an organization yet.')}
          </EmptyDescription>
        </EmptyHeader>
        <EmptyContent>
          {t('Ask an administrator to add you first.')}
        </EmptyContent>
      </Empty>
    </div>
  )
}

function AccessDeniedState() {
  const { t } = useTranslation()
  return (
    <div className='p-4 sm:p-6'>
      <Empty className='min-h-[320px] border'>
        <EmptyHeader>
          <EmptyMedia variant='icon'>
            <Settings />
          </EmptyMedia>
          <EmptyTitle>{t('No permission')}</EmptyTitle>
          <EmptyDescription>
            {t('Your organization role cannot access this page.')}
          </EmptyDescription>
        </EmptyHeader>
      </Empty>
    </div>
  )
}

function LoadingBlock({
  label,
  visibleLabel = false,
}: {
  label: string
  visibleLabel?: boolean
}) {
  return (
    <div className='space-y-4 p-4 sm:p-6' role='status' aria-live='polite'>
      <span
        className={visibleLabel ? 'text-muted-foreground text-sm' : 'sr-only'}
      >
        {label}
      </span>
      <div className='flex items-center justify-between gap-4'>
        <div className='space-y-2'>
          <Skeleton className='h-7 w-48' />
          <Skeleton className='h-4 w-32' />
        </div>
        <Skeleton className='h-6 w-20 rounded-full' />
      </div>
      <Skeleton className='h-24 w-full rounded-lg' />
      <div className='grid gap-3 sm:grid-cols-2 xl:grid-cols-5'>
        {SUMMARY_CARD_SKELETONS.map((slot) => (
          <Skeleton key={slot} className='h-24 rounded-lg' />
        ))}
      </div>
      <div className='grid gap-4 xl:grid-cols-2'>
        <Skeleton className='h-64 rounded-lg' />
        <Skeleton className='h-64 rounded-lg' />
      </div>
    </div>
  )
}

function useOrganizationContext() {
  return useQuery({
    queryKey: organizationKeys.self,
    queryFn: getOrganizationSelf,
  })
}

export function UsageFilters({
  filters,
  options,
  optionsLoading,
  optionsError,
  onRefresh,
  onRetry,
  hasChanges,
  isRefreshing,
  onExport,
  recalculateAction,
  exportHint,
  showMemberFilter,
  showChannelFilter,
}: {
  filters: ReturnType<typeof useUsageFilters>
  options?: OrganizationBillingFilterOptions
  optionsLoading?: boolean
  optionsError?: Error | null
  onRefresh: () => void
  onRetry?: () => void
  hasChanges?: boolean
  isRefreshing?: boolean
  onExport?: () => void
  recalculateAction?: React.ReactNode
  exportHint?: string
  showMemberFilter: boolean
  showChannelFilter: boolean
}) {
  const { t } = useTranslation()
  const memberOptions = useMemo(() => {
    return [...(options?.members ?? [])].sort((a, b) =>
      (a.username || String(a.user_id)).localeCompare(
        b.username || String(b.user_id)
      )
    )
  }, [options?.members])
  const modelOptions = useMemo(
    () =>
      [...(options?.models ?? [])]
        .filter((row) => Boolean(row.model_name))
        .sort((a, b) => (a.model_name ?? '').localeCompare(b.model_name ?? '')),
    [options?.models]
  )
  const channelOptions = useMemo(() => {
    if (!showChannelFilter) return []
    return [...(options?.channels ?? [])]
      .filter((row) => row.channel_id != null)
      .sort((a, b) =>
        (a.channel_name || String(a.channel_id)).localeCompare(
          b.channel_name || String(b.channel_id)
        )
      )
  }, [options?.channels, showChannelFilter])
  let exportControl: React.ReactNode = null
  if (onExport) {
    exportControl = (
      <Button
        variant='outline'
        size='sm'
        onClick={onExport}
        disabled={!filters.isDateRangeValid || hasChanges || isRefreshing}
      >
        <Download />
        {t('Export')}
      </Button>
    )
  }
  if (onExport && exportHint) {
    exportControl = (
      <TooltipProvider>
        <Tooltip>
          <TooltipTrigger
            render={
              <Button
                variant='outline'
                size='sm'
                onClick={onExport}
                disabled={
                  !filters.isDateRangeValid || hasChanges || isRefreshing
                }
              >
                <Download />
                {t('Export')}
              </Button>
            }
          />
          <TooltipContent className='max-w-xs'>{exportHint}</TooltipContent>
        </Tooltip>
      </TooltipProvider>
    )
  }
  return (
    <section className='bg-background rounded-lg border p-4'>
      <div className='text-muted-foreground mb-3 text-xs font-medium tracking-wide'>
        {t('Filters')}
      </div>
      {optionsError ? (
        <ErrorState
          description={optionsError.message}
          onRetry={onRetry ?? onRefresh}
        />
      ) : null}
      <div className='grid items-end gap-3 sm:grid-cols-2 lg:grid-cols-[repeat(5,minmax(0,1fr))_auto]'>
        <UsageDateRangeFields range={filters} />
        {showMemberFilter ? (
          <FieldLabel label={t('User')}>
            <NativeSelect
              className='w-full'
              value={filters.userId}
              disabled={optionsLoading}
              onChange={(event) => {
                filters.setUserId(event.target.value)
              }}
            >
              {optionsLoading ? (
                <NativeSelectOption value=''>
                  {t('Preparing usage statistics...')}
                </NativeSelectOption>
              ) : (
                <NativeSelectOption value=''>{t('All')}</NativeSelectOption>
              )}
              {memberOptions.map((row) => (
                <NativeSelectOption key={row.user_id} value={row.user_id}>
                  {row.username || `${t('User')} #${row.user_id}`}
                </NativeSelectOption>
              ))}
              {filters.userId &&
              !memberOptions.some(
                (row) => String(row.user_id) === filters.userId
              ) ? (
                <NativeSelectOption value={filters.userId}>
                  {t('User')} #{filters.userId}
                </NativeSelectOption>
              ) : null}
            </NativeSelect>
          </FieldLabel>
        ) : null}
        <FieldLabel label={t('Model')}>
          <NativeSelect
            className='w-full'
            value={filters.modelName}
            disabled={optionsLoading}
            onChange={(event) => {
              filters.setModelName(event.target.value)
            }}
          >
            {optionsLoading ? (
              <NativeSelectOption value=''>
                {t('Preparing usage statistics...')}
              </NativeSelectOption>
            ) : (
              <NativeSelectOption value=''>{t('All')}</NativeSelectOption>
            )}
            {modelOptions.map((row) => (
              <NativeSelectOption key={row.model_name} value={row.model_name}>
                {row.model_name}
              </NativeSelectOption>
            ))}
            {filters.modelName &&
            !modelOptions.some(
              (row) => row.model_name === filters.modelName
            ) ? (
              <NativeSelectOption value={filters.modelName}>
                {filters.modelName}
              </NativeSelectOption>
            ) : null}
          </NativeSelect>
        </FieldLabel>
        {showChannelFilter ? (
          <FieldLabel label={t('Channel')}>
            <NativeSelect
              className='w-full'
              value={filters.channelId}
              disabled={optionsLoading}
              onChange={(event) => {
                filters.setChannelId(event.target.value)
              }}
            >
              {optionsLoading ? (
                <NativeSelectOption value=''>
                  {t('Preparing usage statistics...')}
                </NativeSelectOption>
              ) : (
                <NativeSelectOption value=''>{t('All')}</NativeSelectOption>
              )}
              {channelOptions.map((row) => (
                <NativeSelectOption key={row.channel_id} value={row.channel_id}>
                  {row.channel_name || `${t('Channel')} ${row.channel_id}`}
                </NativeSelectOption>
              ))}
              {filters.channelId &&
              !channelOptions.some(
                (row) => String(row.channel_id) === filters.channelId
              ) ? (
                <NativeSelectOption value={filters.channelId}>
                  {t('Channel')} #{filters.channelId}
                </NativeSelectOption>
              ) : null}
            </NativeSelect>
          </FieldLabel>
        ) : null}
        <div className='flex gap-2'>
          <Button
            variant='outline'
            size='sm'
            onClick={onRefresh}
            disabled={!filters.isDateRangeValid || isRefreshing}
            aria-busy={isRefreshing}
          >
            <RefreshCw />
            {t('Query')}
          </Button>
          {recalculateAction}
          {exportControl}
        </div>
      </div>
      <FilterDraftNotice visible={Boolean(hasChanges)} />
    </section>
  )
}

function SummaryGrid({ summary }: { summary?: OrganizationSummary }) {
  const { t } = useTranslation()
  const items = [
    {
      label: t('Requests'),
      value: formatNumber(summary?.request_count),
      description: t('Total requests'),
      icon: Activity,
      iconTone: 'chart-1' as IconBadgeTone,
    },
    {
      label: t('Consumption amount'),
      value: formatBillingAmountFromQuota(summary?.total_quota ?? 0),
      description: t('Total billed amount'),
      icon: Wallet,
      iconTone: 'primary' as IconBadgeTone,
      emphasis: true,
    },
    {
      label: t('Prompt tokens'),
      value: formatNumber(summary?.prompt_tokens),
      description: t('Input tokens'),
      icon: ArrowDownToLine,
      iconTone: 'chart-3' as IconBadgeTone,
    },
    {
      label: t('Completion tokens'),
      value: formatNumber(summary?.completion_tokens),
      description: t('Output tokens'),
      icon: ArrowUpFromLine,
      iconTone: 'chart-4' as IconBadgeTone,
    },
    {
      label: t('Active members'),
      value: formatNumber(summary?.active_member_count),
      description: t('Active in range'),
      icon: Users,
      iconTone: 'success' as IconBadgeTone,
    },
  ]

  return (
    <div className='grid grid-cols-2 gap-3 sm:grid-cols-3 xl:grid-cols-5'>
      {items.map((item) => (
        <BillingStatCard
          key={item.label}
          label={item.label}
          value={item.value}
          description={item.description}
          icon={item.icon}
          iconTone={item.iconTone}
          emphasis={item.emphasis}
        />
      ))}
    </div>
  )
}

function dimensionRowName(
  row: OrganizationDimensionRow,
  t: (key: string) => string
) {
  if (row.display_name && row.username) {
    return `${row.display_name} (${row.username})`
  }
  return (
    row.display_name ||
    row.username ||
    row.model_name ||
    row.channel_name ||
    row.channel_id ||
    (row.user_id != null ? `${t('User')} #${row.user_id}` : undefined) ||
    '-'
  )
}

function dimensionRowKey(row: OrganizationDimensionRow, fallback: string) {
  if (row.user_id != null) return `user:${row.user_id}`
  if (row.model_name != null) return `model:${row.model_name || '__empty__'}`
  if (row.channel_id != null) return `channel:${row.channel_id}`
  return `fallback:${fallback}`
}

function dimensionTokenCount(row: OrganizationDimensionRow) {
  return (row.prompt_tokens ?? 0) + (row.completion_tokens ?? 0)
}

function sharePercentValue(rowQuota: number, totalQuota?: number) {
  if (!totalQuota || totalQuota <= 0) return 0
  return (rowQuota / totalQuota) * 100
}

function formatQuotaShare(rowQuota: number, totalQuota?: number) {
  return formatPercent(sharePercentValue(rowQuota, totalQuota))
}

function formatPricingSnapshot(
  row: OrganizationDimensionRow,
  t: (key: string) => string
) {
  const pricing = row.pricing
  if (!pricing) return '-'
  if (pricing.billing_mode === 'tiered_expr' && pricing.billing_expr) {
    return t('Tiered')
  }
  if (pricing.quota_type === 1) {
    return `${t('Fixed price')} ${formatBillingCurrencyFromUSD(pricing.model_price)}`
  }
  return `${t('Ratio')} ${formatNumber(pricing.model_ratio)}`
}

function DimensionTable(props: {
  rows?: OrganizationDimensionRow[]
  nameLabel: string
  totalQuota?: number
  showPricing?: boolean
}) {
  const { t } = useTranslation()
  return (
    <div className='overflow-x-auto'>
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>{props.nameLabel}</TableHead>
            <TableHead className='text-right'>
              {t('Consumption amount')}
            </TableHead>
            <TableHead className='text-right'>{t('Share')}</TableHead>
            <TableHead className='text-right'>{t('Requests')}</TableHead>
            <TableHead className='text-right'>{t('Prompt tokens')}</TableHead>
            <TableHead className='text-right'>
              {t('Completion tokens')}
            </TableHead>
            <TableHead className='text-right'>{t('Tokens')}</TableHead>
            {props.showPricing ? (
              <TableHead>{t('Current pricing')}</TableHead>
            ) : null}
          </TableRow>
        </TableHeader>
        <TableBody>
          {(props.rows ?? []).map((row) => {
            const rowName = dimensionRowName(row, t)
            return (
              <TableRow key={String(dimensionRowKey(row, String(rowName)))}>
                <TableCell className='max-w-72 min-w-36 truncate'>
                  {rowName}
                </TableCell>
                <TableCell className='text-right whitespace-nowrap tabular-nums'>
                  {formatBillingAmountFromQuota(row.total_quota)}
                </TableCell>
                <TableCell className='text-right whitespace-nowrap'>
                  <div className='ml-auto w-16 lg:w-24'>
                    <div className='tabular-nums'>
                      {formatQuotaShare(row.total_quota, props.totalQuota)}
                    </div>
                    <div className='mt-1.5'>
                      <ProportionBar
                        value={sharePercentValue(
                          row.total_quota,
                          props.totalQuota
                        )}
                        fillClassName='bg-chart-2'
                      />
                    </div>
                  </div>
                </TableCell>
                <TableCell className='text-right whitespace-nowrap tabular-nums'>
                  {formatNumber(row.request_count)}
                </TableCell>
                <TableCell className='text-right whitespace-nowrap tabular-nums'>
                  {formatNumber(row.prompt_tokens)}
                </TableCell>
                <TableCell className='text-right whitespace-nowrap tabular-nums'>
                  {formatNumber(row.completion_tokens)}
                </TableCell>
                <TableCell className='text-right whitespace-nowrap tabular-nums'>
                  {formatNumber(dimensionTokenCount(row))}
                </TableCell>
                {props.showPricing ? (
                  <TableCell className='whitespace-nowrap'>
                    {formatPricingSnapshot(row, t)}
                  </TableCell>
                ) : null}
              </TableRow>
            )
          })}
          {!props.rows?.length ? (
            <EmptyTableRow colSpan={props.showPricing ? 8 : 7} />
          ) : null}
        </TableBody>
      </Table>
    </div>
  )
}

function TrendTable({ rows }: { rows?: OrganizationTrendRow[] }) {
  const { t } = useTranslation()
  const trendRows = rows ?? []
  const maxQuota = trendRows.reduce(
    (max, row) => Math.max(max, row.total_quota ?? 0),
    0
  )
  return (
    <Table>
      <TableHeader>
        <TableRow>
          <TableHead>{`${t('Date')} (${t('Beijing Time')})`}</TableHead>
          <TableHead className='text-right'>
            {t('Consumption amount')}
          </TableHead>
          <TableHead className='text-right'>{t('Requests')}</TableHead>
          <TableHead className='text-right'>{t('Tokens')}</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {trendRows.map((row) => {
          const ratio =
            maxQuota > 0 ? ((row.total_quota ?? 0) / maxQuota) * 100 : 0
          return (
            <TableRow key={row.period}>
              <TableCell className='whitespace-nowrap'>{row.period}</TableCell>
              <TableCell className='text-right whitespace-nowrap'>
                <div className='ml-auto w-20 lg:w-28'>
                  <div className='tabular-nums'>
                    {formatBillingAmountFromQuota(row.total_quota)}
                  </div>
                  <div className='mt-1.5'>
                    <ProportionBar value={ratio} fillClassName='bg-chart-1' />
                  </div>
                </div>
              </TableCell>
              <TableCell className='text-right whitespace-nowrap tabular-nums'>
                {formatNumber(row.request_count)}
              </TableCell>
              <TableCell className='text-right whitespace-nowrap tabular-nums'>
                {formatNumber(
                  (row.prompt_tokens ?? 0) + (row.completion_tokens ?? 0)
                )}
              </TableCell>
            </TableRow>
          )
        })}
        {!trendRows.length ? <EmptyTableRow colSpan={4} /> : null}
      </TableBody>
    </Table>
  )
}

export function UsageSnapshotStatePanels(props: {
  snapshot?: OrganizationUsageSnapshot
  isLoading: boolean
  error?: Error | null
  onRefresh: () => void
}) {
  const { t } = useTranslation()
  const snapshot = props.snapshot
  const payload = snapshot?.payload
  let notice = null
  if (props.error) {
    notice = (
      <ErrorState description={props.error.message} onRetry={props.onRefresh} />
    )
  } else if (props.isLoading && !snapshot) {
    notice = (
      <LoadingBlock label={t('Loading usage statistics...')} visibleLabel />
    )
  } else if (snapshot?.status === 'building') {
    notice = (
      <OrganizationUsageBuildNotice
        sliceDone={snapshot.slice_done}
        sliceTotal={snapshot.slice_total}
        coveredUntil={payload?.covered_until}
      />
    )
  } else if (snapshot?.status === 'failed') {
    notice = (
      <ErrorState
        title={t('Usage statistics failed')}
        description={snapshot.error}
        onRetry={props.onRefresh}
      />
    )
  } else if (snapshot?.status === 'queued' || snapshot?.status === 'paused') {
    notice = (
      <div className='text-muted-foreground text-sm'>
        {snapshot.queue_reason || t('Usage statistics are queued.')}
      </div>
    )
  } else if (!payload) {
    notice = (
      <ErrorState
        title={t('Usage statistics failed')}
        onRetry={props.onRefresh}
      />
    )
  }
  if (!payload) return <Panel title={t('Usage statistics')}>{notice}</Panel>
  return (
    <div className='space-y-4'>
      {notice}
      {snapshot?.status !== 'ready' || props.error ? (
        <div className='text-muted-foreground flex items-center gap-2 text-sm'>
          <Badge variant='secondary'>{t('Previous results')}</Badge>
          {t('Showing the last published generation for this range.')}
        </div>
      ) : null}
      <SummaryGrid summary={payload} />
      <div className='grid gap-4 xl:grid-cols-2'>
        <Panel title={t('Usage trend')}>
          <TrendTable rows={payload.trend} />
        </Panel>
        <Panel title={t('Members')}>
          <DimensionTable
            rows={payload.members}
            nameLabel={t('Member')}
            totalQuota={payload.total_quota}
          />
        </Panel>
        <Panel title={t('Model usage')}>
          <DimensionTable
            rows={payload.models}
            nameLabel={t('Model')}
            totalQuota={payload.total_quota}
            showPricing
          />
          {payload.models_truncated ? <DimensionTruncatedHint /> : null}
        </Panel>
        <Panel title={t('Channel usage')}>
          <DimensionTable
            rows={payload.channels}
            nameLabel={t('Channel')}
            totalQuota={payload.total_quota}
          />
          {payload.channels_truncated ? <DimensionTruncatedHint /> : null}
        </Panel>
      </div>
      <div className='text-muted-foreground text-xs'>
        {t('Coverage through')}
        {': '}
        {formatTimestampInBeijingTime(payload.covered_until)}
      </div>
    </div>
  )
}

function DimensionTruncatedHint() {
  const { t } = useTranslation()
  return (
    <div className='text-muted-foreground mt-2 text-xs'>
      {t(
        'List truncated to the top rows by consumption; totals stay complete.'
      )}
    </div>
  )
}

function UsageLogCursorTable(props: {
  rows?: OrganizationUsageLogItem[]
  options?: OrganizationBillingFilterOptions
}) {
  const { t } = useTranslation()
  const memberNames = useMemo(() => {
    const names = new Map<number, string>()
    for (const member of props.options?.members ?? []) {
      if (member.user_id != null) {
        names.set(
          member.user_id,
          member.display_name || member.username || `${member.user_id}`
        )
      }
    }
    return names
  }, [props.options])
  return (
    <Table>
      <TableHeader>
        <TableRow>
          <TableHead>{`${t('Time')} (${t('Beijing Time')})`}</TableHead>
          <TableHead>{t('User')}</TableHead>
          <TableHead>{t('Model')}</TableHead>
          <TableHead className='text-right'>
            {t('Consumption amount')}
          </TableHead>
          <TableHead className='text-right'>{t('Prompt tokens')}</TableHead>
          <TableHead className='text-right'>{t('Completion tokens')}</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {(props.rows ?? []).map((row) => (
          <TableRow key={row.id}>
            <TableCell>
              {formatTimestampInBeijingTime(row.created_at)}
            </TableCell>
            <TableCell>
              {memberNames.get(row.user_id) || `${t('User')} #${row.user_id}`}
            </TableCell>
            <TableCell>{row.model_name || '-'}</TableCell>
            <TableCell className='text-right whitespace-nowrap tabular-nums'>
              {formatBillingAmountFromQuota(row.quota)}
            </TableCell>
            <TableCell className='text-right whitespace-nowrap tabular-nums'>
              {formatNumber(row.prompt_tokens)}
            </TableCell>
            <TableCell className='text-right whitespace-nowrap tabular-nums'>
              {formatNumber(row.completion_tokens)}
            </TableCell>
          </TableRow>
        ))}
        {!props.rows?.length ? <EmptyTableRow colSpan={6} /> : null}
      </TableBody>
    </Table>
  )
}

function CursorPager(props: {
  hasMore: boolean
  canGoBack: boolean
  isFetching: boolean
  onPrev: () => void
  onNext: () => void
}) {
  const { t } = useTranslation()
  return (
    <div className='flex items-center justify-end gap-3 pt-3'>
      <Button
        variant='outline'
        size='sm'
        disabled={!props.canGoBack || props.isFetching}
        onClick={props.onPrev}
      >
        {t('Previous')}
      </Button>
      <Button
        variant='outline'
        size='sm'
        disabled={!props.hasMore || props.isFetching}
        onClick={props.onNext}
      >
        {t('Next')}
      </Button>
    </div>
  )
}

function EmptyTableRow({ colSpan }: { colSpan: number }) {
  const { t } = useTranslation()
  return (
    <TableRow>
      <TableCell
        colSpan={colSpan}
        className='text-muted-foreground h-24 text-center'
      >
        {t('No data')}
      </TableCell>
    </TableRow>
  )
}

function Pager({
  page,
  total,
  pageSize,
  onPageChange,
}: {
  page: number
  total: number
  pageSize: number
  onPageChange: (page: number) => void
}) {
  const { t } = useTranslation()
  const pageCount = Math.max(1, Math.ceil(total / pageSize))
  return (
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
  )
}

function UserSearchPicker({
  selectedUser,
  onSelect,
}: {
  selectedUser: User | null
  onSelect: (user: User) => void
}) {
  const { t } = useTranslation()
  const [keyword, setKeyword] = useState('')
  const usersQuery = useQuery({
    queryKey: ['organization', 'user-search', keyword],
    queryFn: () =>
      searchUsers({
        keyword,
        role: String(USER_ROLE.USER),
        page_size: 8,
      }),
    enabled: keyword.trim().length > 0,
  })
  const users = usersQuery.data?.data?.items ?? []

  return (
    <div className='space-y-3'>
      <div className='relative'>
        <Search className='text-muted-foreground absolute top-2.5 left-2.5 size-4' />
        <Input
          value={keyword}
          onChange={(event) => setKeyword(event.target.value)}
          className='pl-8'
          placeholder={t('Search users')}
        />
      </div>
      {selectedUser ? (
        <div className='rounded-lg border p-3 text-sm'>
          <div className='font-medium'>
            {selectedUser.display_name || selectedUser.username}
          </div>
          <div className='text-muted-foreground'>
            {selectedUser.username} · ID {selectedUser.id}
          </div>
        </div>
      ) : null}
      <div className='max-h-56 overflow-auto rounded-lg border'>
        {users.map((user) => (
          <button
            key={user.id}
            type='button'
            className='hover:bg-muted flex w-full items-center justify-between gap-3 border-b p-3 text-left text-sm last:border-0'
            onClick={() => onSelect(user)}
          >
            <span className='min-w-0'>
              <span className='block truncate font-medium'>
                {user.display_name || user.username}
              </span>
              <span className='text-muted-foreground block truncate'>
                {user.username} · ID {user.id}
              </span>
            </span>
            {selectedUser?.id === user.id ? (
              <Badge>{t('Selected')}</Badge>
            ) : null}
          </button>
        ))}
        {keyword && !usersQuery.isLoading && users.length === 0 ? (
          <div className='text-muted-foreground p-4 text-center text-sm'>
            {t('No users found')}
          </div>
        ) : null}
      </div>
    </div>
  )
}

function MemberDialog({
  open,
  onOpenChange,
  onSubmit,
  isPending,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  onSubmit: (userId: number, role: OrganizationRole) => void
  isPending: boolean
}) {
  const { t } = useTranslation()
  const [selectedUser, setSelectedUser] = useState<User | null>(null)
  const [role, setRole] = useState<OrganizationRole>('member')

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className='sm:max-w-lg'>
        <DialogHeader>
          <DialogTitle>{t('Add organization member')}</DialogTitle>
          <DialogDescription>
            {t('Search a user and choose an organization role.')}
          </DialogDescription>
        </DialogHeader>
        <UserSearchPicker
          selectedUser={selectedUser}
          onSelect={setSelectedUser}
        />
        <NativeSelect
          className='w-full'
          value={role}
          onChange={(event) => setRole(event.target.value as OrganizationRole)}
        >
          {ROLE_OPTIONS.map((item) => (
            <NativeSelectOption key={item} value={item}>
              {roleLabel(item, t)}
            </NativeSelectOption>
          ))}
        </NativeSelect>
        <DialogFooter>
          <Button
            disabled={!selectedUser || isPending}
            onClick={() => selectedUser && onSubmit(selectedUser.id, role)}
          >
            <UserPlus />
            {t('Add member')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function SettingsDialog({
  organization,
  open,
  onOpenChange,
  onSubmit,
  isPending,
  canEditStatus,
}: {
  organization: Organization
  open: boolean
  onOpenChange: (open: boolean) => void
  onSubmit: (payload: { name: string; status?: OrganizationStatus }) => void
  isPending: boolean
  canEditStatus?: boolean
}) {
  const { t } = useTranslation()
  const [name, setName] = useState(organization.name)
  const [status, setStatus] = useState(String(organization.status))

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t('Organization settings')}</DialogTitle>
          <DialogDescription>
            {t('Update the organization name and status.')}
          </DialogDescription>
        </DialogHeader>
        <div className='space-y-3'>
          <Input
            value={name}
            onChange={(event) => setName(event.target.value)}
          />
          {canEditStatus ? (
            <NativeSelect
              className='w-full'
              value={status}
              onChange={(event) => setStatus(event.target.value)}
            >
              <NativeSelectOption value={String(ORGANIZATION_STATUS_ENABLED)}>
                {t('Active')}
              </NativeSelectOption>
              <NativeSelectOption value={String(ORGANIZATION_STATUS_DISABLED)}>
                {t('Suspended')}
              </NativeSelectOption>
            </NativeSelect>
          ) : null}
        </div>
        <DialogFooter>
          <Button
            disabled={!name.trim() || isPending}
            onClick={() =>
              onSubmit({
                name: name.trim(),
                status: canEditStatus
                  ? (Number(status) as OrganizationStatus)
                  : undefined,
              })
            }
          >
            {t('Save')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function BillingStartAdjustDialog({
  member,
  organizationId,
  open,
  onOpenChange,
  onApplied,
}: {
  member: OrganizationMember | null
  organizationId?: number
  open: boolean
  onOpenChange: (open: boolean) => void
  onApplied: () => void
}) {
  const { t } = useTranslation()
  const currentEffective = member
    ? member.billing_start_at || member.joined_at
    : 0
  const [candidateDate, setCandidateDate] = useState('')
  const [preview, setPreview] =
    useState<OrganizationBillingStartPreview | null>(null)

  useEffect(() => {
    if (member) {
      setCandidateDate(unixTimestampToBeijingDateInput(currentEffective))
      setPreview(null)
    }
  }, [member, currentEffective])

  const previewMutation = useMutation({
    mutationFn: ({
      userId,
      candidate,
    }: {
      userId: number
      candidate: number
    }) =>
      organizationId !== undefined
        ? previewAdminOrganizationMemberBillingStart(
            organizationId,
            userId,
            candidate
          )
        : previewCurrentOrganizationMemberBillingStart(userId, candidate),
    onSuccess: (res) => {
      if (!res.success) return
      setPreview(res.data ?? null)
    },
  })

  const applyMutation = useMutation({
    mutationFn: ({
      userId,
      payload,
    }: {
      userId: number
      payload: OrganizationBillingStartUpdatePayload
    }) =>
      organizationId !== undefined
        ? updateAdminOrganizationMemberBillingStart(
            organizationId,
            userId,
            payload
          )
        : updateCurrentOrganizationMemberBillingStart(userId, payload),
    onSuccess: (res) => {
      if (!res.success) return
      toast.success(t('Billing start updated'))
      onOpenChange(false)
      onApplied()
    },
  })

  const candidate = candidateDate ? (dateToUnix(candidateDate) ?? 0) : 0
  const conflict = Boolean(preview?.conflict)
  const previewMatches = preview?.candidate_billing_start === candidate
  const canApply = Boolean(preview) && previewMatches && !conflict

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className='sm:max-w-lg'>
        <DialogHeader>
          <DialogTitle>{t('Adjust billing start')}</DialogTitle>
          <DialogDescription>
            {t(
              'This only affects organization reports, not personal wallet or completed charges.'
            )}
          </DialogDescription>
        </DialogHeader>
        <div className='space-y-3'>
          <FieldLabel
            label={`${t('Candidate billing start')} (${t('Beijing Time')})`}
          >
            <Input
              type='date'
              value={candidateDate}
              max={unixTimestampToBeijingDateInput(member?.joined_at || 0)}
              onChange={(event) => {
                setCandidateDate(event.target.value)
                setPreview(null)
              }}
            />
          </FieldLabel>
          {preview ? (
            <div className='bg-muted/30 space-y-1.5 rounded-md p-3 text-sm'>
              <div className='flex justify-between gap-4'>
                <span className='text-muted-foreground'>
                  {t('Added requests')}
                </span>
                <span>{preview.added_request_count}</span>
              </div>
              <div className='flex justify-between gap-4'>
                <span className='text-muted-foreground'>
                  {t('Added quota')}
                </span>
                <span>{preview.added_quota}</span>
              </div>
              {preview.earliest_log_at ? (
                <div className='flex justify-between gap-4'>
                  <span className='text-muted-foreground'>
                    {`${t('Log range')} (${t('Beijing Time')})`}
                  </span>
                  <span className='whitespace-nowrap'>
                    {formatTimestampInBeijingTime(preview.earliest_log_at)} ~{' '}
                    {formatTimestampInBeijingTime(preview.latest_log_at)}
                  </span>
                </div>
              ) : null}
              {preview.earliest_retained_at ? (
                <div className='flex justify-between gap-4'>
                  <span className='text-muted-foreground'>
                    {`${t('Earliest retained log')} (${t('Beijing Time')})`}
                  </span>
                  <span>
                    {formatTimestampInBeijingTime(preview.earliest_retained_at)}
                  </span>
                </div>
              ) : null}
              {conflict ? (
                <div className='text-destructive'>{t('Window conflict')}</div>
              ) : null}
            </div>
          ) : null}
        </div>
        <DialogFooter>
          <Button
            variant='outline'
            disabled={
              !candidate ||
              !member ||
              candidate === currentEffective ||
              previewMutation.isPending
            }
            onClick={() =>
              member &&
              candidate &&
              previewMutation.mutate({ userId: member.user_id, candidate })
            }
          >
            {t('Preview')}
          </Button>
          <Button
            disabled={!canApply || !member || applyMutation.isPending}
            onClick={() =>
              member &&
              preview &&
              applyMutation.mutate({
                userId: member.user_id,
                payload: {
                  candidate_billing_start: preview.candidate_billing_start,
                  expected_billing_start: preview.current_billing_start,
                },
              })
            }
          >
            {t('Apply')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function MembersTable({
  members,
  currentRole,
  onRoleChange,
  onRemove,
  onAdjustBillingStart,
  isMutating,
}: {
  members?: OrganizationMember[]
  currentRole?: OrganizationRole
  onRoleChange: (userId: number, role: OrganizationRole) => void
  onRemove: (member: OrganizationMember) => void
  onAdjustBillingStart: (member: OrganizationMember) => void
  isMutating: boolean
}) {
  const { t } = useTranslation()
  const canEdit = canManageMembers(currentRole)

  return (
    <Table>
      <TableHeader>
        <TableRow>
          <TableHead>{t('User')}</TableHead>
          <TableHead>{t('Role')}</TableHead>
          <TableHead>{`${t('Joined at')} (${t('Beijing Time')})`}</TableHead>
          <TableHead>{`${t('Billing start')} (${t('Beijing Time')})`}</TableHead>
          <TableHead>{t('Status')}</TableHead>
          <TableHead className='text-right'>{t('Actions')}</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {(members ?? []).map((member) => {
          const displayName =
            member.display_name || member.username || `ID ${member.user_id}`
          const secondaryName = member.username
            ? `${member.username} · ID ${member.user_id}`
            : `ID ${member.user_id}`
          const secondaryInfo = member.email
            ? `${secondaryName} · ${member.email}`
            : secondaryName
          const disabled = isMutating || !canEdit || Boolean(member.left_at)
          return (
            <TableRow key={`${member.user_id}-${member.left_at ?? 0}`}>
              <TableCell>
                <div className='flex items-center gap-3'>
                  <EntityAvatar name={displayName} />
                  <div className='min-w-0'>
                    <div className='truncate font-medium'>{displayName}</div>
                    <div className='text-muted-foreground truncate text-xs'>
                      {secondaryInfo}
                    </div>
                  </div>
                </div>
              </TableCell>
              <TableCell>
                {canEdit ? (
                  <NativeSelect
                    size='sm'
                    value={member.role}
                    disabled={disabled}
                    onChange={(event) =>
                      onRoleChange(
                        member.user_id,
                        event.target.value as OrganizationRole
                      )
                    }
                  >
                    {ROLE_OPTIONS.map((item) => (
                      <NativeSelectOption key={item} value={item}>
                        {roleLabel(item, t)}
                      </NativeSelectOption>
                    ))}
                  </NativeSelect>
                ) : (
                  <Badge variant={roleBadgeVariant(member.role)}>
                    {roleLabel(member.role, t)}
                  </Badge>
                )}
              </TableCell>
              <TableCell className='whitespace-nowrap'>
                {formatTimestampInBeijingTime(member.joined_at)}
              </TableCell>
              <TableCell className='whitespace-nowrap'>
                {formatTimestampInBeijingTime(
                  member.billing_start_at || member.joined_at
                )}
              </TableCell>
              <TableCell>
                {member.left_at ? (
                  <DotBadge tone='muted'>{t('Removed')}</DotBadge>
                ) : (
                  <DotBadge tone='success'>{t('Active')}</DotBadge>
                )}
              </TableCell>
              <TableCell className='text-right'>
                <div className='flex items-center justify-end gap-1'>
                  <Button
                    variant='ghost'
                    size='icon-sm'
                    disabled={disabled || Boolean(member.left_at)}
                    onClick={() => onAdjustBillingStart(member)}
                  >
                    <CalendarClock />
                    <span className='sr-only'>{t('Adjust billing start')}</span>
                  </Button>
                  <Button
                    variant='ghost'
                    size='icon-sm'
                    disabled={disabled || Boolean(member.left_at)}
                    onClick={() => onRemove(member)}
                  >
                    <Trash2 />
                    <span className='sr-only'>{t('Remove')}</span>
                  </Button>
                </div>
              </TableCell>
            </TableRow>
          )
        })}
        {!members?.length ? <EmptyTableRow colSpan={6} /> : null}
      </TableBody>
    </Table>
  )
}

export function OrganizationUsagePage() {
  const { t } = useTranslation()
  const contextQuery = useOrganizationContext()
  const self = contextQuery.data?.data
  const role = self?.member.role
  const usage = useOrganizationUsageQuery({
    organizationId: self?.organization.id,
    mode: 'billing',
    enabled: Boolean(self && canViewBilling(role)),
  })
  const {
    filters,
    directoryOptions,
    directoryLoading,
    directoryError,
    usageSnapshotQuery,
  } = usage
  const canViewChannels = canViewOrganizationBillingChannels(role)
  const usageSnapshot = usageSnapshotQuery.data?.data

  if (contextQuery.isLoading) return <LoadingBlock label={t('Loading...')} />
  if (!self) return <OrganizationEmptyState />
  if (!canViewBilling(role)) return <AccessDeniedState />

  return (
    <SectionPageLayout>
      <OrgSectionTitle
        title={t('Organization billing')}
        name={self.organization.name}
      />
      <SectionPageLayout.Actions>
        <DotBadge tone={orgStatusTone(self.organization.status)}>
          {statusLabel(self.organization.status, t)}
        </DotBadge>
      </SectionPageLayout.Actions>
      <SectionPageLayout.Content>
        <div className='space-y-4'>
          <UsageFilters
            filters={filters}
            options={directoryOptions}
            optionsLoading={directoryLoading}
            optionsError={directoryError}
            showMemberFilter={role === 'admin'}
            showChannelFilter={canViewChannels}
            onRefresh={usage.refresh}
            onRetry={usage.retry}
            hasChanges={usage.hasChanges}
            isRefreshing={usage.isRefreshing}
            recalculateAction={
              <OrganizationRecalculateButton
                kind='usage'
                coveredUntil={usageSnapshot?.range_end}
                organizationId={self?.organization.id}
                scope={`${filters.startDate} – ${filters.endDate}`}
                filters={filters}
                pending={usage.isRecalculating}
                disabled={
                  usage.hasChanges ||
                  usage.isRefreshing ||
                  !usageSnapshot?.snapshot_id ||
                  usageSnapshot.status === 'building'
                }
                onConfirm={usage.recalculate}
              />
            }
            exportHint={t('Usage statistics')}
            onExport={
              usageSnapshot?.payload
                ? () => {
                    void downloadOrganizationExport(
                      buildUsageSnapshotExportUrl(
                        usage.appliedParams,
                        usageSnapshot
                      )
                    )
                  }
                : undefined
            }
          />
          <UsageSnapshotStatePanels
            snapshot={usageSnapshotQuery.data?.data}
            isLoading={usageSnapshotQuery.isLoading}
            error={usageSnapshotQuery.error}
            onRefresh={usage.retry}
          />
        </div>
      </SectionPageLayout.Content>
    </SectionPageLayout>
  )
}

export function OrganizationMembersPage() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [showHistory, setShowHistory] = useState(false)
  const [memberDialogOpen, setMemberDialogOpen] = useState(false)
  const [settingsOpen, setSettingsOpen] = useState(false)
  const [removingMember, setRemovingMember] =
    useState<OrganizationMember | null>(null)
  const [adjustingMember, setAdjustingMember] =
    useState<OrganizationMember | null>(null)
  const contextQuery = useOrganizationContext()
  const self = contextQuery.data?.data
  const role = self?.member.role
  const enabled = Boolean(self && canManageMembers(role))

  const membersQuery = useQuery({
    queryKey: organizationKeys.members(showHistory),
    queryFn: () => getCurrentOrganizationMembers(showHistory),
    enabled,
  })

  const invalidate = () => {
    void queryClient.invalidateQueries({ queryKey: ['organization'] })
  }

  const addMutation = useMutation({
    mutationFn: ({
      userId,
      memberRole,
    }: {
      userId: number
      memberRole: OrganizationRole
    }) => addCurrentOrganizationMember({ user_id: userId, role: memberRole }),
    onSuccess: (res) => {
      if (!res.success) return
      toast.success(t('Member added'))
      setMemberDialogOpen(false)
      invalidate()
    },
  })
  const roleMutation = useMutation({
    mutationFn: ({
      userId,
      memberRole,
    }: {
      userId: number
      memberRole: OrganizationRole
    }) => updateCurrentOrganizationMember(userId, { role: memberRole }),
    onSuccess: (res) => {
      if (!res.success) return
      toast.success(t('Role updated'))
      invalidate()
    },
  })
  const removeMutation = useMutation({
    mutationFn: (userId: number) => removeCurrentOrganizationMember(userId),
    onSuccess: (res) => {
      if (!res.success) return
      toast.success(t('Member removed'))
      setRemovingMember(null)
      invalidate()
    },
  })
  const settingsMutation = useMutation({
    mutationFn: updateCurrentOrganization,
    onSuccess: (res) => {
      if (!res.success) return
      toast.success(t('Organization updated'))
      setSettingsOpen(false)
      invalidate()
    },
  })

  if (contextQuery.isLoading) return <LoadingBlock label={t('Loading...')} />
  if (!self) return <OrganizationEmptyState />
  if (!canManageMembers(role)) return <AccessDeniedState />

  return (
    <SectionPageLayout>
      <OrgSectionTitle
        title={t('Organization members')}
        name={self.organization.name}
      />
      <SectionPageLayout.Actions>
        <Button variant='outline' onClick={() => setSettingsOpen(true)}>
          <Settings />
          {t('Settings')}
        </Button>
        <Button onClick={() => setMemberDialogOpen(true)}>
          <Plus />
          {t('Add member')}
        </Button>
      </SectionPageLayout.Actions>
      <SectionPageLayout.Content>
        <div className='space-y-4'>
          <Panel
            title={t('Members')}
            description={t('Manage roles and organization membership.')}
            actions={
              <NativeSelect
                size='sm'
                value={showHistory ? 'history' : 'active'}
                onChange={(event) =>
                  setShowHistory(event.target.value === 'history')
                }
              >
                <NativeSelectOption value='active'>
                  {t('Active')}
                </NativeSelectOption>
                <NativeSelectOption value='history'>
                  {t('Include removed')}
                </NativeSelectOption>
              </NativeSelect>
            }
          >
            <MembersTable
              members={membersQuery.data?.data}
              currentRole={role}
              isMutating={
                addMutation.isPending ||
                roleMutation.isPending ||
                removeMutation.isPending
              }
              onRoleChange={(userId, memberRole) =>
                roleMutation.mutate({ userId, memberRole })
              }
              onRemove={setRemovingMember}
              onAdjustBillingStart={setAdjustingMember}
            />
          </Panel>
          <MemberDialog
            open={memberDialogOpen}
            onOpenChange={setMemberDialogOpen}
            isPending={addMutation.isPending}
            onSubmit={(userId, memberRole) =>
              addMutation.mutate({ userId, memberRole })
            }
          />
          <BillingStartAdjustDialog
            member={adjustingMember}
            open={Boolean(adjustingMember)}
            onOpenChange={(open) => !open && setAdjustingMember(null)}
            onApplied={invalidate}
          />
          <SettingsDialog
            organization={self.organization}
            open={settingsOpen}
            onOpenChange={setSettingsOpen}
            isPending={settingsMutation.isPending}
            onSubmit={(payload) => settingsMutation.mutate(payload)}
          />
          <ConfirmDialog
            open={Boolean(removingMember)}
            onOpenChange={(open) => !open && setRemovingMember(null)}
            title={t('Remove member')}
            desc={t('This user will lose access to the organization.')}
            destructive
            isLoading={removeMutation.isPending}
            handleConfirm={() =>
              removingMember && removeMutation.mutate(removingMember.user_id)
            }
          />
        </div>
      </SectionPageLayout.Content>
    </SectionPageLayout>
  )
}

export function OrganizationLogsPage() {
  const { t } = useTranslation()
  const contextQuery = useOrganizationContext()
  const self = contextQuery.data?.data
  const role = self?.member.role
  const usage = useOrganizationUsageQuery({
    organizationId: self?.organization.id,
    mode: 'logs',
    enabled: Boolean(self && canViewBilling(role)),
  })
  const {
    filters,
    directoryOptions,
    directoryLoading,
    directoryError,
    logsQuery,
    logsParams,
    logsCursorStack,
    setLogsCursorStack,
  } = usage

  if (contextQuery.isLoading) return <LoadingBlock label={t('Loading...')} />
  if (!self) return <OrganizationEmptyState />
  if (!canViewBilling(role)) return <AccessDeniedState />

  const logsData = logsQuery.data?.data

  return (
    <SectionPageLayout>
      <OrgSectionTitle
        title={t('Organization billing logs')}
        name={self.organization.name}
      />
      <SectionPageLayout.Content>
        <div className='space-y-4'>
          <UsageFilters
            filters={filters}
            options={directoryOptions}
            optionsLoading={directoryLoading}
            optionsError={directoryError}
            showMemberFilter={role === 'admin'}
            showChannelFilter={false}
            onRefresh={usage.refresh}
            onRetry={usage.retry}
            hasChanges={usage.hasChanges}
            isRefreshing={usage.isRefreshing}
            onExport={
              logsData
                ? () => {
                    void downloadOrganizationExport(
                      buildOrganizationLogsExportUrl({
                        ...logsParams,
                        start_timestamp: logsData.range_start,
                        end_timestamp: logsData.range_end - 1,
                      })
                    )
                  }
                : undefined
            }
          />
          <Panel title={t('Billing logs')}>
            {logsQuery.error ? (
              <ErrorState
                description={logsQuery.error.message}
                onRetry={usage.retry}
              />
            ) : (
              <UsageLogCursorTable
                rows={logsData?.items}
                options={directoryOptions}
              />
            )}
            <CursorPager
              hasMore={logsData?.has_more ?? false}
              canGoBack={logsCursorStack.length > 0}
              isFetching={logsQuery.isFetching || logsQuery.isError}
              onPrev={() => setLogsCursorStack((stack) => stack.slice(0, -1))}
              onNext={() => {
                if (logsData?.next_cursor) {
                  setLogsCursorStack((stack) => [
                    ...stack,
                    logsData.next_cursor as string,
                  ])
                }
              }}
            />
          </Panel>
        </div>
      </SectionPageLayout.Content>
    </SectionPageLayout>
  )
}

function CreateOrganizationDialog({
  open,
  onOpenChange,
  onSubmit,
  isPending,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  onSubmit: (name: string) => void
  isPending: boolean
}) {
  const { t } = useTranslation()
  const [name, setName] = useState('')

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className='sm:max-w-lg'>
        <DialogHeader>
          <DialogTitle>{t('Create organization')}</DialogTitle>
          <DialogDescription>
            {t('Create an organization first, then add members later.')}
          </DialogDescription>
        </DialogHeader>
        <Input
          value={name}
          onChange={(event) => setName(event.target.value)}
          placeholder={t('Organization name')}
        />
        <DialogFooter>
          <Button
            disabled={!name.trim() || isPending}
            onClick={() => onSubmit(name.trim())}
          >
            <Plus />
            {t('Create')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

export function AdminOrganizationsPage() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [keyword, setKeyword] = useState('')
  const [status, setStatus] = useState('')
  const [page, setPage] = useState(1)
  const [createOpen, setCreateOpen] = useState(false)
  const params = useMemo(
    () => ({ p: page, page_size: 20, keyword, status }),
    [keyword, page, status]
  )

  const organizationsQuery = useQuery({
    queryKey: organizationKeys.organizations(params),
    queryFn: () => getAdminOrganizations(params),
  })
  const createMutation = useMutation({
    mutationFn: (name: string) => createAdminOrganization({ name }),
    onSuccess: (res) => {
      if (!res.success) return
      toast.success(t('Organization created'))
      setCreateOpen(false)
      void queryClient.invalidateQueries({
        queryKey: ['admin', 'organizations'],
      })
    },
  })

  const pageData = organizationsQuery.data?.data

  return (
    <div className='flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto p-4 sm:p-6'>
      <PageHeader
        title={t('Organizations')}
        description={t('Manage organizations, owners, members, and billing.')}
        actions={
          <Button onClick={() => setCreateOpen(true)}>
            <Plus />
            {t('Create organization')}
          </Button>
        }
      />
      <div className='grid gap-2 sm:grid-cols-[minmax(0,1fr)_180px_auto]'>
        <Input
          value={keyword}
          onChange={(event) => {
            setKeyword(event.target.value)
            setPage(1)
          }}
          placeholder={t('Search organizations')}
        />
        <NativeSelect
          className='w-full'
          value={status}
          onChange={(event) => {
            setStatus(event.target.value)
            setPage(1)
          }}
        >
          <NativeSelectOption value=''>{t('All statuses')}</NativeSelectOption>
          <NativeSelectOption value={String(ORGANIZATION_STATUS_ENABLED)}>
            {t('Active')}
          </NativeSelectOption>
          <NativeSelectOption value={String(ORGANIZATION_STATUS_DISABLED)}>
            {t('Suspended')}
          </NativeSelectOption>
        </NativeSelect>
        <Button
          variant='outline'
          size='icon'
          onClick={() => void organizationsQuery.refetch()}
        >
          <RefreshCw />
          <span className='sr-only'>{t('Refresh')}</span>
        </Button>
      </div>
      <Panel title={t('Organizations')}>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{t('Name')}</TableHead>
              <TableHead>{t('Status')}</TableHead>
              <TableHead>{t('Updated at')}</TableHead>
              <TableHead className='text-right'>{t('Actions')}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {(pageData?.items ?? []).map((organization) => (
              <TableRow key={organization.id}>
                <TableCell>
                  <div className='flex items-center gap-3'>
                    <EntityAvatar name={organization.name} size='sm' />
                    <div className='min-w-0'>
                      <div className='truncate font-medium'>
                        {organization.name}
                      </div>
                      <div className='text-muted-foreground text-xs'>
                        ID {organization.id}
                      </div>
                    </div>
                  </div>
                </TableCell>
                <TableCell>
                  <DotBadge tone={orgStatusTone(organization.status)}>
                    {statusLabel(organization.status, t)}
                  </DotBadge>
                </TableCell>
                <TableCell>
                  {formatTimestampToDate(organization.updated_at)}
                </TableCell>
                <TableCell className='text-right'>
                  <Button
                    variant='outline'
                    size='sm'
                    nativeButton={false}
                    render={
                      <Link
                        to='/admin/organizations/$id'
                        params={{ id: String(organization.id) }}
                      />
                    }
                  >
                    {t('Manage')}
                  </Button>
                </TableCell>
              </TableRow>
            ))}
            {!pageData?.items?.length ? <EmptyTableRow colSpan={4} /> : null}
          </TableBody>
        </Table>
        <Pager
          page={pageData?.page ?? page}
          total={pageData?.total ?? 0}
          pageSize={pageData?.page_size ?? 20}
          onPageChange={setPage}
        />
      </Panel>
      <CreateOrganizationDialog
        open={createOpen}
        onOpenChange={setCreateOpen}
        isPending={createMutation.isPending}
        onSubmit={(name) => createMutation.mutate(name)}
      />
    </div>
  )
}

export function AdminOrganizationDetailPage(props: { id: number }) {
  return <AdminOrganizationDetailView key={props.id} id={props.id} />
}

function AdminOrganizationDetailView({ id }: { id: number }) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  // 组织折扣是 Root 专属能力：普通 Admin 不显示折扣页签（后端同样返回 403）。
  const isRoot = useAuthStore(
    (state) => state.auth.user?.role === ROLE.SUPER_ADMIN
  )
  const [tab, setTab] = useState<OrganizationDetailTab>('members')
  const [invoiceVisited, setInvoiceVisited] = useState(false)
  const [memberDialogOpen, setMemberDialogOpen] = useState(false)
  const [settingsOpen, setSettingsOpen] = useState(false)
  const [showHistory, setShowHistory] = useState(false)
  const [removingMember, setRemovingMember] =
    useState<OrganizationMember | null>(null)
  const [adjustingMember, setAdjustingMember] =
    useState<OrganizationMember | null>(null)
  const billingUsage = useOrganizationUsageQuery({
    organizationId: id,
    admin: true,
    mode: 'billing',
    enabled: tab === 'billing',
  })
  const logUsage = useOrganizationUsageQuery({
    organizationId: id,
    admin: true,
    mode: 'logs',
    enabled: tab === 'logs',
  })
  const usage = tab === 'logs' ? logUsage : billingUsage
  const {
    filters,
    directoryError,
    directoryOptions,
    directoryLoading,
    logsParams,
    logsCursorStack,
    setLogsCursorStack,
  } = usage
  const usageSnapshotQuery = billingUsage.usageSnapshotQuery
  const logsQuery = logUsage.logsQuery
  const usageSnapshot = usageSnapshotQuery.data?.data

  const organizationQuery = useQuery({
    queryKey: organizationKeys.adminDetail(id),
    queryFn: () => getAdminOrganization(id),
  })
  const membersQuery = useQuery({
    queryKey: organizationKeys.adminMembers(id, showHistory),
    queryFn: () => getAdminOrganizationMembers(id, showHistory),
  })
  const invalidate = () => {
    void queryClient.invalidateQueries({
      queryKey: ['admin', 'organizations', id],
    })
    void queryClient.invalidateQueries({ queryKey: ['admin', 'organizations'] })
  }

  const addMutation = useMutation({
    mutationFn: ({
      userId,
      memberRole,
    }: {
      userId: number
      memberRole: OrganizationRole
    }) => addAdminOrganizationMember(id, { user_id: userId, role: memberRole }),
    onSuccess: (res) => {
      if (!res.success) return
      toast.success(t('Member added'))
      setMemberDialogOpen(false)
      invalidate()
    },
  })
  const roleMutation = useMutation({
    mutationFn: ({
      userId,
      memberRole,
    }: {
      userId: number
      memberRole: OrganizationRole
    }) => updateAdminOrganizationMember(id, userId, { role: memberRole }),
    onSuccess: (res) => {
      if (!res.success) return
      toast.success(t('Role updated'))
      invalidate()
    },
  })
  const removeMutation = useMutation({
    mutationFn: (userId: number) => removeAdminOrganizationMember(id, userId),
    onSuccess: (res) => {
      if (!res.success) return
      toast.success(t('Member removed'))
      setRemovingMember(null)
      invalidate()
    },
  })
  const settingsMutation = useMutation({
    mutationFn: (payload: { name: string; status?: OrganizationStatus }) =>
      updateAdminOrganization(id, payload),
    onSuccess: (res) => {
      if (!res.success) return
      toast.success(t('Organization updated'))
      setSettingsOpen(false)
      invalidate()
    },
  })

  const organization = organizationQuery.data?.data
  const logsData = logsQuery.data?.data

  if (organizationQuery.isLoading) {
    return <LoadingBlock label={t('Loading...')} />
  }
  if (!organization) {
    return <OrganizationEmptyState />
  }

  return (
    <div className='flex min-h-0 flex-1 flex-col gap-6 overflow-y-auto p-4 sm:p-6'>
      <header className='border-b'>
        <div className='flex flex-col gap-4 pb-4 sm:flex-row sm:items-center sm:justify-between'>
          <div className='flex min-w-0 flex-wrap items-center gap-x-4 gap-y-1'>
            <EntityAvatar name={organization.name} />
            <h1 className='truncate text-2xl font-semibold tracking-tight'>
              {organization.name}
            </h1>
            <Separator
              orientation='vertical'
              className='hidden h-6 self-center sm:block'
            />
            <p className='text-muted-foreground text-sm'>
              {t('Organization')} ID {organization.id}
            </p>
          </div>
          <div className='flex shrink-0 items-center gap-2'>
            <DotBadge tone={orgStatusTone(organization.status)}>
              {statusLabel(organization.status, t)}
            </DotBadge>
            <Button variant='outline' onClick={() => setSettingsOpen(true)}>
              <Settings data-icon='inline-start' />
              {t('Settings')}
            </Button>
          </div>
        </div>
        <Tabs
          value={tab}
          onValueChange={(value) => {
            setTab(value as OrganizationDetailTab)
            if (value === 'invoice') setInvoiceVisited(true)
          }}
          className='gap-0'
        >
          <TabsList
            variant='line'
            className='h-10 gap-6 p-0 group-data-horizontal/tabs:h-10'
          >
            {(
              [
                'members',
                'billing',
                'invoice',
                'logs',
                ...(isRoot ? (['discount'] as const) : []),
              ] as OrganizationDetailTab[]
            ).map((item) => (
              <TabsTrigger
                key={item}
                value={item}
                className='data-active:text-primary after:bg-primary min-w-14 px-0'
              >
                {organizationDetailTabLabel(item, t)}
              </TabsTrigger>
            ))}
          </TabsList>
        </Tabs>
      </header>
      {tab === 'members' ? (
        <section className='bg-background shrink-0 overflow-hidden rounded-xl border'>
          <div className='bg-muted/30 flex flex-col gap-3 border-b p-4 sm:flex-row sm:items-center sm:justify-between sm:p-5'>
            <h2 className='text-lg font-semibold tracking-tight'>
              {t('Members')}
            </h2>
            <div className='flex shrink-0 items-center gap-2'>
              <NativeSelect
                value={showHistory ? 'history' : 'active'}
                onChange={(event) =>
                  setShowHistory(event.target.value === 'history')
                }
              >
                <NativeSelectOption value='active'>
                  {t('Active')}
                </NativeSelectOption>
                <NativeSelectOption value='history'>
                  {t('Include removed')}
                </NativeSelectOption>
              </NativeSelect>
              <Button onClick={() => setMemberDialogOpen(true)}>
                <Plus data-icon='inline-start' />
                {t('Add member')}
              </Button>
            </div>
          </div>
          <div className='[&_[data-slot=table-header]]:bg-muted/20 [&_[data-slot=table-body]>tr]:h-20 [&_[data-slot=table-cell]]:px-5 [&_[data-slot=table-head]]:h-12 [&_[data-slot=table-head]]:px-5'>
            <MembersTable
              members={membersQuery.data?.data}
              currentRole='admin'
              isMutating={
                addMutation.isPending ||
                roleMutation.isPending ||
                removeMutation.isPending
              }
              onRoleChange={(userId, memberRole) =>
                roleMutation.mutate({ userId, memberRole })
              }
              onRemove={setRemovingMember}
              onAdjustBillingStart={setAdjustingMember}
            />
          </div>
        </section>
      ) : null}
      {tab === 'billing' ? (
        <div className='space-y-4'>
          <UsageFilters
            filters={filters}
            options={directoryOptions}
            optionsLoading={directoryLoading}
            optionsError={directoryError}
            showMemberFilter
            showChannelFilter
            onRefresh={usage.refresh}
            onRetry={usage.retry}
            hasChanges={usage.hasChanges}
            isRefreshing={usage.isRefreshing}
            recalculateAction={
              <OrganizationRecalculateButton
                kind='usage'
                coveredUntil={usageSnapshot?.range_end}
                organizationId={id}
                scope={`${filters.startDate} – ${filters.endDate}`}
                filters={filters}
                pending={usage.isRecalculating}
                disabled={
                  usage.hasChanges ||
                  usage.isRefreshing ||
                  !usageSnapshot?.snapshot_id ||
                  usageSnapshot.status === 'building'
                }
                onConfirm={usage.recalculate}
              />
            }
            exportHint={t('Usage statistics')}
            onExport={
              usageSnapshot?.payload
                ? () => {
                    void downloadOrganizationExport(
                      buildUsageSnapshotExportUrl(
                        usage.appliedParams,
                        usageSnapshot,
                        id
                      )
                    )
                  }
                : undefined
            }
          />
          <UsageSnapshotStatePanels
            snapshot={usageSnapshotQuery.data?.data}
            isLoading={usageSnapshotQuery.isLoading}
            error={usageSnapshotQuery.error}
            onRefresh={usage.retry}
          />
        </div>
      ) : null}
      {invoiceVisited ? (
        <div hidden={tab !== 'invoice'}>
          <OrganizationInvoicePanel
            organizationId={id}
            enabled={tab === 'invoice'}
          />
        </div>
      ) : null}
      {tab === 'logs' ? (
        <div className='space-y-4'>
          <UsageFilters
            filters={filters}
            options={directoryOptions}
            optionsLoading={directoryLoading}
            optionsError={directoryError}
            showMemberFilter
            showChannelFilter={false}
            onRefresh={usage.refresh}
            onRetry={usage.retry}
            hasChanges={usage.hasChanges}
            isRefreshing={usage.isRefreshing}
            onExport={
              logsData
                ? () => {
                    void downloadOrganizationExport(
                      buildAdminOrganizationLogsExportUrl(id, {
                        ...logsParams,
                        start_timestamp: logsData.range_start,
                        end_timestamp: logsData.range_end - 1,
                      })
                    )
                  }
                : undefined
            }
          />
          <Panel title={t('Billing logs')}>
            {logsQuery.error ? (
              <ErrorState
                description={logsQuery.error.message}
                onRetry={usage.retry}
              />
            ) : (
              <UsageLogCursorTable
                rows={logsData?.items}
                options={directoryOptions}
              />
            )}
            <CursorPager
              hasMore={logsData?.has_more ?? false}
              canGoBack={logsCursorStack.length > 0}
              isFetching={logsQuery.isFetching || logsQuery.isError}
              onPrev={() => setLogsCursorStack((stack) => stack.slice(0, -1))}
              onNext={() => {
                if (logsData?.next_cursor) {
                  setLogsCursorStack((stack) => [
                    ...stack,
                    logsData.next_cursor as string,
                  ])
                }
              }}
            />
          </Panel>
        </div>
      ) : null}
      {tab === 'discount' && isRoot ? (
        <OrganizationDiscountPanel organizationId={id} />
      ) : null}
      <MemberDialog
        open={memberDialogOpen}
        onOpenChange={setMemberDialogOpen}
        isPending={addMutation.isPending}
        onSubmit={(userId, memberRole) =>
          addMutation.mutate({ userId, memberRole })
        }
      />
      <BillingStartAdjustDialog
        member={adjustingMember}
        organizationId={id}
        open={Boolean(adjustingMember)}
        onOpenChange={(open) => !open && setAdjustingMember(null)}
        onApplied={invalidate}
      />
      <SettingsDialog
        organization={organization}
        open={settingsOpen}
        onOpenChange={setSettingsOpen}
        isPending={settingsMutation.isPending}
        canEditStatus
        onSubmit={(payload) => settingsMutation.mutate(payload)}
      />
      <ConfirmDialog
        open={Boolean(removingMember)}
        onOpenChange={(open) => !open && setRemovingMember(null)}
        title={t('Remove member')}
        desc={t('This user will lose access to the organization.')}
        destructive
        isLoading={removeMutation.isPending}
        handleConfirm={() =>
          removingMember && removeMutation.mutate(removingMember.user_id)
        }
      />
    </div>
  )
}
