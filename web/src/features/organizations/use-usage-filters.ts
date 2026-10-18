import { useMemo, useState } from 'react'

import { beijingDateBoundaryToUnix } from './beijing-time'
import type { OrganizationUsageParams } from './types'
import { useUsageDateRange } from './use-usage-date-range'

export function dateToUnix(value: string) {
  return beijingDateBoundaryToUnix(value, false)
}

export function unixEndOfDate(value: string) {
  const lastSecond = beijingDateBoundaryToUnix(value, true)
  return lastSecond === undefined ? undefined : lastSecond + 1
}

export function useUsageFilters(initialRange?: {
  startDate: string
  endDate: string
}) {
  const dateRange = useUsageDateRange({ initialRange })
  const { startDate, endDate } = dateRange
  const [userId, setUserId] = useState('')
  const [modelName, setModelName] = useState('')
  const [channelId, setChannelId] = useState('')

  const params = useMemo<OrganizationUsageParams>(
    () => ({
      start_timestamp: dateToUnix(startDate),
      end_timestamp: unixEndOfDate(endDate),
      user_id: userId ? Number(userId) : undefined,
      model_name: modelName || undefined,
      channel: channelId ? Number(channelId) : undefined,
    }),
    [channelId, endDate, modelName, startDate, userId]
  )

  return {
    ...dateRange,
    userId,
    setUserId,
    modelName,
    setModelName,
    channelId,
    setChannelId,
    params,
  }
}
