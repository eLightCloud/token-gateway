import type { BillDimension, BillFilters, BillGroupRow } from './types'

export function filtersForBillGroup(
  filters: BillFilters,
  row: BillGroupRow
): BillFilters {
  const next = { ...filters, page: 1 }

  if (
    filters.dimension === 'user' ||
    filters.dimension === 'user_model' ||
    filters.dimension === 'user_channel'
  ) {
    next.userId = row.user_id ?? 0
  }
  if (
    filters.dimension === 'channel' ||
    filters.dimension === 'channel_model' ||
    filters.dimension === 'user_channel' ||
    filters.dimension === 'upstream_channel' ||
    filters.dimension === 'upstream_channel_model'
  ) {
    next.channelId = row.channel_id ?? 0
  }
  if (
    filters.dimension === 'user_model' ||
    filters.dimension === 'channel_model' ||
    filters.dimension === 'upstream_channel_model'
  ) {
    next.modelName = row.model_name ?? ''
  }
  if (
    filters.dimension === 'upstream_channel' ||
    filters.dimension === 'upstream_channel_model'
  ) {
    next.apiAddress = row.api_address || '__unknown__'
  }

  return next
}

export function billDetailBreakdownDimension(
  dimension: BillDimension
): BillDimension | undefined {
  switch (dimension) {
    case 'user':
    case 'user_channel':
      return 'user_model'
    case 'user_model':
      return 'user_channel'
    case 'channel':
      return 'channel_model'
    case 'upstream_channel':
      return 'upstream_channel_model'
    default:
      return undefined
  }
}
