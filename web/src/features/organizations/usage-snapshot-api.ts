import { t } from 'i18next'

import { api } from '@/lib/api'

import { buildQuery } from './api'
import type {
  ApiResponse,
  OrganizationDimensionRow,
  OrganizationUsageParams,
} from './types'

// Unified billing-page snapshot: one request returns the status of the
// background build and, when ready, the materialized rollup shared by every
// region of the billing tab (summary, trend, members, models, channels).
// Field names match the legacy per-endpoint responses so the rendering
// components stay unchanged.

export type OrganizationUsageSnapshotStatus =
  | 'building'
  | 'ready'
  | 'failed'
  | 'invalidated'
  | 'queued'
  | 'paused'

export interface OrganizationUsageTrendPoint {
  period: string
  total_quota: number
  request_count: number
  prompt_tokens: number
  completion_tokens: number
}

export interface OrganizationUsagePayload {
  total_quota: number
  request_count: number
  prompt_tokens: number
  completion_tokens: number
  member_count: number
  active_member_count: number
  usage_user_count: number
  trend: OrganizationUsageTrendPoint[]
  members: OrganizationDimensionRow[]
  models: OrganizationDimensionRow[]
  channels: OrganizationDimensionRow[]
  models_truncated: boolean
  channels_truncated: boolean
  covered_until: number
  range_start: number
  range_end: number
  generated_at: number
  calculation_version: number
}

export interface OrganizationUsageSnapshot {
  status: OrganizationUsageSnapshotStatus
  payload_snapshot_id?: number
  snapshot_id: number
  revision: number
  payload_generation: number
  range_start: number
  range_end: number
  requested_end: number
  slice_done: number
  slice_total: number
  failure_kind?: string
  queue_reason?: string
  error?: string
  payload?: OrganizationUsagePayload
}

export async function getAdminOrganizationBillingUsageSnapshot(
  id: number,
  params: OrganizationUsageParams,
  refresh = false,
  signal?: AbortSignal,
  snapshotId?: number
): Promise<ApiResponse<OrganizationUsageSnapshot>> {
  const query = buildQuery({
    ...params,
    refresh: refresh || undefined,
    snapshot_id: snapshotId,
  })
  const res = await api.get(
    `/api/admin/organizations/${id}/billing/usage?${query}`,
    { signal, disableDuplicate: Boolean(signal) }
  )
  return res.data
}

export async function getCurrentOrganizationBillingUsageSnapshot(
  params: OrganizationUsageParams,
  refresh = false,
  signal?: AbortSignal,
  snapshotId?: number
): Promise<ApiResponse<OrganizationUsageSnapshot>> {
  const query = buildQuery({
    ...params,
    refresh: refresh || undefined,
    snapshot_id: snapshotId,
  })
  const res = await api.get(
    `/api/organization/current/billing/usage?${query}`,
    { signal, disableDuplicate: Boolean(signal) }
  )
  return res.data
}

export interface OrganizationUsageLogItem {
  id: number
  user_id: number
  created_at: number
  type: number
  model_name: string
  channel_id: number
  quota: number
  prompt_tokens: number
  completion_tokens: number
}

export interface OrganizationUsageLogCursorPage {
  items: OrganizationUsageLogItem[]
  next_cursor?: string
  has_more: boolean
  range_start: number
  range_end: number
}

export async function getAdminOrganizationBillingUsageLogs(
  id: number,
  params: OrganizationUsageParams & { cursor?: string; page_size?: number },
  signal?: AbortSignal
): Promise<ApiResponse<OrganizationUsageLogCursorPage>> {
  const query = buildQuery(params)
  const res = await api.get(
    `/api/admin/organizations/${id}/billing/logs/cursor?${query}`,
    { signal, disableDuplicate: Boolean(signal) }
  )
  return res.data
}

export async function getCurrentOrganizationBillingUsageLogs(
  params: OrganizationUsageParams & { cursor?: string; page_size?: number },
  signal?: AbortSignal
): Promise<ApiResponse<OrganizationUsageLogCursorPage>> {
  const query = buildQuery(params)
  const res = await api.get(
    `/api/organization/current/billing/logs/cursor?${query}`,
    { signal, disableDuplicate: Boolean(signal) }
  )
  return res.data
}

export function buildUsageSnapshotExportUrl(
  params: OrganizationUsageParams,
  snapshot: OrganizationUsageSnapshot,
  organizationId?: number
) {
  if (!snapshot.payload) throw new Error(t('Usage statistics failed'))
  const prefix =
    organizationId === undefined
      ? '/api/organization/current'
      : `/api/admin/organizations/${organizationId}`
  return `${prefix}/billing/usage/export?${buildQuery({
    ...params,
    start_timestamp: snapshot.payload.range_start,
    end_timestamp: snapshot.payload.range_end,
    snapshot_id: snapshot.payload_snapshot_id ?? snapshot.snapshot_id,
    generation: snapshot.payload_generation,
  })}`
}
