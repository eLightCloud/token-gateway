import { queryOptions, useQuery, useQueryClient } from '@tanstack/react-query'
import { useId, useState } from 'react'
import { toast } from 'sonner'

import { api } from '@/lib/api'
import {
  getServerErrorMessage,
  getServerErrorSources,
  getServerErrorStatus,
  requireServerSuccess,
} from '@/lib/server-error-message'

import type { UsageLog } from '../data/schema'
import { buildQueryParams } from './query-params'
import { buildApiParams } from './utils'

type PersonalLogPage = {
  items: UsageLog[]
  record_keys: string[]
  next_cursor: string
}

type PersonalLogScroll = { scope: string; cursors: string[]; version: number }

function isPersonalLogPageUnavailable(error: unknown): boolean {
  const status = getServerErrorStatus(error)
  return (
    status === 404 ||
    ((status === undefined || status === 200) &&
      getServerErrorSources(error).some(
        (source) =>
          source.message_key === 'Personal log pagination is unavailable'
      ))
  )
}

export function usePersonalLogPage(config: {
  enabled: boolean
  userId: number | undefined
  pageSize: number
  searchParams: Record<string, unknown>
  columnFilters: Array<{ id: string; value: unknown }>
}) {
  const client = useQueryClient()
  const session = useId()
  const params = buildApiParams({ ...config, page: 1, isAdmin: false })
  if (!config.searchParams.startTime && !config.searchParams.endTime) {
    delete params.start_timestamp
    delete params.end_timestamp
  }
  delete params.p
  const search = buildQueryParams({ ...params })
  const scope = `${session}:${config.userId}:${search}`
  const [saved, setSaved] = useState<PersonalLogScroll>()
  const [moving, setMoving] = useState(false)
  const [unavailableFor, setUnavailableFor] = useState<number>()
  const unavailable = unavailableFor != null && unavailableFor === config.userId
  const scroll =
    saved?.scope === scope
      ? saved
      : { scope, cursors: [''], version: saved?.version ?? 0 }

  const optionsFor = (target: PersonalLogScroll) => {
    const cursor = target.cursors.at(-1) ?? ''
    return queryOptions({
      queryKey: ['personal-log-page', scope, target.version, cursor],
      queryFn: async ({ signal }) => {
        const url = `/api/log/self/page?${search}&cursor=${encodeURIComponent(cursor)}`
        let page: PersonalLogPage
        try {
          const response = await api.get<{
            success: boolean
            data: PersonalLogPage
          }>(url, { signal })
          page = requireServerSuccess(response.data).data
        } catch (error) {
          if (signal.aborted || !isPersonalLogPageUnavailable(error)) {
            throw error
          }
          setUnavailableFor(config.userId)
          return null
        }
        return {
          rows: page.items.map((row, index) => ({
            ...row,
            record_key: page.record_keys[index],
          })),
          nextCursor: page.next_cursor,
        }
      },
      retry: false,
      staleTime: Infinity,
    })
  }

  const query = useQuery({
    ...optionsFor(scroll),
    enabled: config.enabled && !unavailable,
  })

  async function move(target: PersonalLogScroll): Promise<void> {
    if (moving) return
    setMoving(true)
    try {
      const page = await client.fetchQuery(optionsFor(target))
      if (page) setSaved(target)
    } catch (error) {
      toast.error(getServerErrorMessage(error, 'Failed to load logs'))
    } finally {
      setMoving(false)
    }
  }

  const latest = { ...scroll, cursors: [''], version: scroll.version + 1 }
  const next = query.data?.nextCursor
  return {
    query,
    moving,
    unavailable,
    canGoBack: scroll.cursors.length > 1,
    canGoNext: !!next,
    startSearch: () => setSaved(latest),
    refresh: () => void move(latest),
    previous: () =>
      void move({ ...scroll, cursors: scroll.cursors.slice(0, -1) }),
    next: () => {
      if (next) void move({ ...scroll, cursors: [...scroll.cursors, next] })
    },
  }
}
