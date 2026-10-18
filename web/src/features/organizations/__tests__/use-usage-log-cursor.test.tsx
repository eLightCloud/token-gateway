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
  QueryClient,
  QueryClientProvider,
  useQuery,
} from '@tanstack/react-query'
import { act, renderHook, waitFor } from '@testing-library/react'
import type { PropsWithChildren } from 'react'
import { describe, expect, test, vi } from 'vitest'

import { organizationKeys } from '../api'
import type { OrganizationUsageParams } from '../types'
import { useUsageLogCursor } from '../use-usage-log-cursor'

const filterChanges: [string, OrganizationUsageParams][] = [
  ['user', { user_id: 152 }],
  ['model', { model_name: 'claude-opus-4-6' }],
  ['start date', { start_timestamp: 1790784000 }],
  ['end date', { end_timestamp: 1792339200 }],
]

function renderLogQuery(admin: boolean) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Infinity } },
  })
  const request = vi.fn(
    async (_params: OrganizationUsageParams, _cursor: string) => ({ items: [] })
  )
  function Wrapper(props: PropsWithChildren) {
    return (
      <QueryClientProvider client={client}>
        {props.children}
      </QueryClientProvider>
    )
  }
  const hook = renderHook(
    (props: { organizationId: number; params: OrganizationUsageParams }) => {
      const baseKey = admin
        ? organizationKeys.adminUsageLogsCursor(
            props.organizationId,
            props.params,
            ''
          )
        : organizationKeys.usageLogsCursor(
            props.params,
            '',
            props.organizationId
          )
      const [cursors, setCursors] = useUsageLogCursor(baseKey)
      const cursor = cursors.at(-1) ?? ''
      const query = useQuery({
        queryKey: [...baseKey, cursor],
        queryFn: () => request(props.params, cursor),
      })
      return { cursors, setCursors, query }
    },
    { wrapper: Wrapper, initialProps: { organizationId: 1, params: {} } }
  )
  return { ...hook, request }
}

describe.each([true, false])('organization log cursors (admin=%s)', (admin) => {
  test.each(filterChanges)(
    'changing %s requests only the new first page',
    async (_name, params) => {
      const hook = renderLogQuery(admin)
      await waitFor(() =>
        expect(hook.result.current.query.isSuccess).toBe(true)
      )
      act(() => hook.result.current.setCursors(['all-users-next-page']))
      await waitFor(() =>
        expect(hook.request).toHaveBeenCalledWith({}, 'all-users-next-page')
      )
      hook.request.mockClear()

      hook.rerender({ organizationId: 1, params })
      await waitFor(() => expect(hook.request).toHaveBeenCalledWith(params, ''))
      expect(hook.request.mock.calls).toEqual([[params, '']])
      expect(hook.result.current.cursors).toEqual([])

      // Returning to the previous filters must not restore their old page.
      hook.rerender({ organizationId: 1, params: {} })
      expect(hook.result.current.cursors).toEqual([])
    }
  )

  test('changing organization never replays the previous organization cursor', async () => {
    const hook = renderLogQuery(admin)
    await waitFor(() => expect(hook.result.current.query.isSuccess).toBe(true))
    act(() => hook.result.current.setCursors(['organization-1-next-page']))
    await waitFor(() =>
      expect(hook.request).toHaveBeenCalledWith({}, 'organization-1-next-page')
    )
    hook.request.mockClear()

    hook.rerender({ organizationId: 2, params: {} })
    await waitFor(() => expect(hook.request).toHaveBeenCalledWith({}, ''))
    expect(hook.request.mock.calls).toEqual([[{}, '']])
    expect(hook.result.current.cursors).toEqual([])
  })

  test('equivalent filters preserve paging; previous and reset keep working', async () => {
    const hook = renderLogQuery(admin)
    await waitFor(() => expect(hook.result.current.query.isSuccess).toBe(true))
    act(() => hook.result.current.setCursors(['page-2']))
    await waitFor(() => expect(hook.request).toHaveBeenCalledWith({}, 'page-2'))
    act(() => hook.result.current.setCursors((stack) => [...stack, 'page-3']))
    await waitFor(() => expect(hook.request).toHaveBeenCalledWith({}, 'page-3'))

    hook.rerender({ organizationId: 1, params: { user_id: undefined } })
    expect(hook.result.current.cursors).toEqual(['page-2', 'page-3'])
    act(() => hook.result.current.setCursors((stack) => stack.slice(0, -1)))
    expect(hook.result.current.cursors).toEqual(['page-2'])
    act(() => hook.result.current.setCursors([]))
    expect(hook.result.current.cursors).toEqual([])
  })
})
