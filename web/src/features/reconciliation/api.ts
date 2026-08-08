import { api } from '@/lib/api'

import type {
  ApiResult,
  BillEntriesPage,
  BillFilters,
  BillGroupsPage,
  BillSummary,
} from './types'

const base = '/api/reconciliation'

export const billKeys = {
  all: ['token-bill'] as const,
  summary: (filters: BillFilters) =>
    [
      'token-bill',
      'summary',
      filters.startTimestamp,
      filters.endTimestamp,
      filters.perspective,
      filters.billType,
      filters.organizationId,
      filters.modelName,
      filters.channelId,
      filters.apiAddress,
      filters.requestId,
    ] as const,
  groups: (filters: BillFilters) => ['token-bill', 'groups', filters] as const,
  entries: (filters: BillFilters) =>
    ['token-bill', 'entries', filters] as const,
}

function queryParams(filters: BillFilters, includePage: boolean) {
  const params = {
    start_timestamp: filters.startTimestamp,
    end_timestamp: filters.endTimestamp,
    perspective: filters.perspective,
    dimension: filters.dimension,
    type: filters.billType === 'all' ? undefined : filters.billType,
    organization_id: filters.organizationId,
    user_id: filters.userId,
    model_name: filters.modelName,
    channel_id: filters.channelId,
    api_address: filters.apiAddress,
    request_id: filters.requestId,
  }
  if (!includePage) return params
  return { ...params, p: filters.page, page_size: filters.pageSize }
}

async function getData<T>(
  path: string,
  filters: BillFilters,
  includePage = false
): Promise<T> {
  const response = await api.get<ApiResult<T>>(`${base}${path}`, {
    params: queryParams(filters, includePage),
    skipBusinessError: true,
  })
  if (!response.data.success) {
    throw new Error(response.data.message || 'Unable to load token bill')
  }
  return response.data.data
}

export function getBillSummary(filters: BillFilters) {
  return getData<BillSummary>('/summary', filters)
}

export function getBillEntries(filters: BillFilters) {
  return getData<BillEntriesPage>('/entries', filters, true)
}

export function getBillGroups(filters: BillFilters) {
  return getData<BillGroupsPage>('/groups', filters, true)
}

export async function downloadBillCSV(filters: BillFilters): Promise<void> {
  const response = await api.get(`${base}/export.csv`, {
    params: queryParams(filters, false),
    responseType: 'blob',
  })
  const blob = response.data as Blob
  if (blob.type?.includes('application/json')) {
    const payload = JSON.parse(await blob.text()) as { message?: string }
    throw new Error(payload.message || 'Unable to export token bill')
  }
  const objectURL = URL.createObjectURL(blob)
  const link = document.createElement('a')
  link.href = objectURL
  const disposition = response.headers['content-disposition'] as
    | string
    | undefined
  const filename = disposition?.match(/filename="?([^";]+)"?/i)?.[1]
  link.download = filename || 'token-bill.csv'
  document.body.appendChild(link)
  link.click()
  link.remove()
  URL.revokeObjectURL(objectURL)
}
