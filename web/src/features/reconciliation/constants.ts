import type { BillDimension, BillPerspective, BillType } from './types'

export const BILL_TYPES: ReadonlyArray<{
  value: BillType
  labelKey: string
}> = [
  { value: 'all', labelKey: 'All bill types' },
  { value: 'consume', labelKey: 'Consumption' },
  { value: 'refund', labelKey: 'Refund' },
]

export const BILL_DEFAULT_PAGE_SIZE = 20

export const BILL_PERSPECTIVES: ReadonlyArray<{
  value: BillPerspective
  labelKey: string
}> = [
  { value: 'customer', labelKey: 'Customer bill' },
  { value: 'upstream', labelKey: 'Channel usage' },
  { value: 'api_address', labelKey: 'By upstream' },
]

export const BILL_DIMENSIONS: Record<
  BillPerspective,
  ReadonlyArray<{ value: BillDimension; labelKey: string }>
> = {
  customer: [
    { value: 'user', labelKey: 'By customer' },
    { value: 'user_model', labelKey: 'Customer × model' },
    { value: 'user_channel', labelKey: 'Customer × channel' },
  ],
  upstream: [
    { value: 'channel', labelKey: 'By channel' },
    { value: 'channel_model', labelKey: 'Channel × model' },
  ],
  api_address: [
    { value: 'upstream_channel', labelKey: 'API address × channel' },
    { value: 'upstream_channel_model', labelKey: 'Upstream × model' },
  ],
}
