export type BillType = 'all' | 'consume' | 'refund'
export type BillPerspective = 'customer' | 'upstream' | 'api_address'
export type BillDimension =
  | 'user'
  | 'user_model'
  | 'user_channel'
  | 'channel'
  | 'channel_model'
  | 'upstream_channel'
  | 'upstream_channel_model'

export type BillFilters = {
  startTimestamp: number
  endTimestamp: number
  perspective: BillPerspective
  dimension: BillDimension
  billType: BillType
  organizationId?: number
  userId?: number
  modelName?: string
  channelId?: number
  apiAddress?: string
  requestId?: string
  page: number
  pageSize: number
}

export type BillFilterOption = {
  value: number | string
  label: string
}

export type BillSummary = {
  perspective: BillPerspective
  consume_logging_enabled: boolean
  net_quota: number
  consume_quota: number
  refund_quota: number
  record_count: number
  prompt_tokens: number
  completion_tokens: number
  filter_options: {
    organizations: BillFilterOption[]
    models: BillFilterOption[]
    channels: BillFilterOption[]
  }
}

export type BillGroupRow = {
  key: string
  label: string
  user_id?: number
  username?: string
  channel_id?: number
  channel_name?: string
  model_name?: string
  api_address?: string
  record_count: number
  prompt_tokens: number
  completion_tokens: number
  quota: number
}

export type BillGroupsPage = {
  items: BillGroupRow[]
  total: number
  page: number
  page_size: number
  dimension: BillDimension
}

export type BillEntry = {
  id: number
  user_id: number
  token_id: number
  channel_id: number
  created_at: number
  type: Exclude<BillType, 'all'>
  request_id: string
  upstream_request_id: string
  organization_name: string
  username: string
  token_name: string
  model_name: string
  channel_name: string
  channel_api_address: string
  prompt_tokens: number
  completion_tokens: number
  quota: number
}

export type BillEntriesPage = {
  items: BillEntry[]
  total: number
  page: number
  page_size: number
}

export type ApiResult<T> = {
  success: boolean
  message?: string
  data: T
}
