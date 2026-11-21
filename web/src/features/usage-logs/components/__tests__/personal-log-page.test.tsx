import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router'
import {
  act,
  cleanup,
  render,
  renderHook,
  screen,
  waitFor,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactNode } from 'react'
import { afterEach, expect, test, vi } from 'vitest'

import { SectionPageLayout } from '@/components/layout'
import { api } from '@/lib/api'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'
import { useSystemConfigStore } from '@/stores/system-config-store'

import { usageLogSchema } from '../../data/schema'
import { usePersonalLogPage } from '../../lib/personal-log-page'
import { UsageLogsProvider } from '../usage-logs-provider'
import { UsageLogsTable } from '../usage-logs-table'

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
  useAuthStore.setState(useAuthStore.getInitialState(), true)
  useSystemConfigStore.setState(useSystemConfigStore.getInitialState(), true)
  localStorage.clear()
})

test('reads bounded latest pages, retains order and forwards exact request IDs', async () => {
  const request = vi.spyOn(api, 'get')
  const response = (requestId: string, nextCursor = '') => ({
    data: {
      success: true,
      data: {
        items: [{ id: 1, request_id: requestId }],
        record_keys: [requestId],
        next_cursor: nextCursor,
      },
    },
  })
  request
    .mockResolvedValueOnce(response('first', 'after-first'))
    .mockResolvedValueOnce(response('older'))
    .mockResolvedValueOnce(response('just-written', 'after-new'))
    .mockResolvedValueOnce(response('find-this'))

  const client = new QueryClient()
  const { result, rerender } = renderHook(
    ({ requestId }: { requestId?: string }) =>
      usePersonalLogPage({
        enabled: true,
        userId: 42,
        pageSize: 20,
        searchParams: { requestId },
        columnFilters: [],
      }),
    {
      initialProps: { requestId: undefined as string | undefined },
      wrapper: ({ children }: { children: ReactNode }) => (
        <QueryClientProvider client={client}>{children}</QueryClientProvider>
      ),
    }
  )
  await waitFor(() => expect(result.current.canGoNext).toBe(true))
  expect(request.mock.calls[0]?.[0]).toBe(
    '/api/log/self/page?page_size=20&cursor='
  )

  act(() => result.current.next())
  await waitFor(() => expect(result.current.canGoBack).toBe(true))
  expect(result.current.query.data?.rows[0]?.record_key).toBe('older')
  expect(request.mock.calls[1]?.[0]).toContain('cursor=after-first')

  act(() => result.current.previous())
  await waitFor(() => expect(result.current.canGoBack).toBe(false))
  expect(request).toHaveBeenCalledTimes(2)

  act(() => result.current.refresh())
  await waitFor(() =>
    expect(result.current.query.data?.rows[0]?.record_key).toBe('just-written')
  )
  expect(request).toHaveBeenCalledTimes(3)

  rerender({ requestId: 'find-this' })
  await waitFor(() =>
    expect(result.current.query.data?.rows[0]?.request_id).toBe('find-this')
  )
  expect(request.mock.calls[3]?.[0]).toContain('request_id=find-this')
  expect(
    request.mock.calls.every(([url]) => !url.includes('start_timestamp'))
  ).toBe(true)
})

function renderPersonalLogs(
  options: {
    entry?: string
    cursorFailure?: 'disabled' | 'missing' | 'busy' | 'forbidden'
    failStats?: boolean
  } = {}
) {
  useAuthStore
    .getState()
    .auth.setUser({ id: 42, username: 'tester', role: ROLE.USER })
  const logCalls: string[] = []
  const log = usageLogSchema.parse({
    id: 1,
    user_id: 42,
    type: 2,
    created_at: 1788840000,
    content: '',
    request_id: 'find-this',
  })
  vi.spyOn(api, 'get').mockImplementation(async (url) => {
    if (url === '/api/status') {
      return {
        data: { success: true, data: { personal_log_cursor_enabled: true } },
      }
    }
    if (url.startsWith('/api/log/')) logCalls.push(url)
    if (url.startsWith('/api/log/self/page?')) {
      switch (options.cursorFailure) {
        case 'disabled':
          return {
            data: {
              success: false,
              message_key: 'Personal log pagination is unavailable',
            },
          }
        case 'missing':
          if (url.includes('cursor=after-first')) {
            throw { response: { status: 404 } }
          }
          break
        case 'busy':
          return {
            data: {
              success: false,
              message_key: 'The log service is busy. Please try again.',
            },
          }
        case 'forbidden':
          throw {
            response: { status: 403, data: { message: 'Access denied' } },
          }
      }
      return {
        data: {
          success: true,
          data: {
            items: [{ ...log, model_name: 'cursor-result' }],
            record_keys: ['stable-row'],
            next_cursor: 'after-first',
          },
        },
      }
    }
    if (url.startsWith('/api/log/self?')) {
      return {
        data: {
          success: true,
          data: { items: [{ ...log, model_name: 'legacy-result' }], total: 1 },
        },
      }
    }
    if (url.startsWith('/api/log/self/stat?')) {
      if (options.failStats) {
        return {
          data: { success: false, message_key: 'Failed to query statistics' },
        }
      }
      return { data: { success: true, data: { rpm: 7, tpm: 100, quota: 5 } } }
    }
    return { data: { success: true, data: {} } }
  })

  const root = createRootRoute()
  const auth = createRoute({ getParentRoute: () => root, id: '_authenticated' })
  const logs = createRoute({
    getParentRoute: () => auth,
    path: '/usage-logs/$section',
    component: () => (
      <SectionPageLayout fixedContent>
        <SectionPageLayout.Content>
          <UsageLogsTable logCategory='common' />
        </SectionPageLayout.Content>
      </SectionPageLayout>
    ),
    validateSearch: (search: Record<string, unknown>) => search,
  })
  const router = createRouter({
    routeTree: root.addChildren([auth.addChildren([logs])]),
    history: createMemoryHistory({
      initialEntries: [options.entry ?? '/usage-logs/common'],
    }),
  })
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <UsageLogsProvider>
        <RouterProvider router={router} />
      </UsageLogsProvider>
    </QueryClientProvider>
  )
  return logCalls
}

test('opens and refreshes the latest page without reading statistics until requested', async () => {
  const calls = renderPersonalLogs()
  const user = userEvent.setup()
  await screen.findAllByText('cursor-result')
  expect(calls).toEqual(['/api/log/self/page?page_size=100&cursor='])

  await user.click(screen.getByRole('button', { name: 'Refresh latest' }))
  await waitFor(() => expect(calls).toHaveLength(2))
  expect(calls[1]).toBe(calls[0])

  await user.click(
    screen.getByRole('button', {
      name: 'Calculate statistics for selected range',
    })
  )
  await screen.findByText('RPM')
  expect(calls[2]).toMatch(/^\/api\/log\/self\/stat\?/)
})

test('falls back at first load when the receiving node disables cursor paging', async () => {
  const calls = renderPersonalLogs({
    entry: '/usage-logs/common?requestId=find-this&pageSize=20',
    cursorFailure: 'disabled',
  })
  await screen.findAllByText('legacy-result')
  expect(calls).toHaveLength(2)
  expect(calls[0]).toMatch(/^\/api\/log\/self\/page\?/)
  expect(calls[1]).toMatch(/^\/api\/log\/self\?/)
  const filters = new URL(calls[1], 'http://localhost').searchParams
  expect(filters.get('request_id')).toBe('find-this')
  expect(filters.get('page_size')).toBe('20')
  expect(screen.queryByRole('button', { name: 'Refresh latest' })).toBeNull()
})

test('falls back when the next page reaches a node without the cursor route', async () => {
  const calls = renderPersonalLogs({ cursorFailure: 'missing' })
  await screen.findAllByText('cursor-result')
  await userEvent.click(screen.getByRole('button', { name: 'Next' }))
  await screen.findAllByText('legacy-result')
  expect(calls).toHaveLength(3)
  expect(calls[1]).toContain('cursor=after-first')
  expect(calls[2]).toMatch(/^\/api\/log\/self\?/)
})

test.each(['busy', 'forbidden'] as const)(
  'keeps cursor mode when the request is %s',
  async (cursorFailure) => {
    const calls = renderPersonalLogs({ cursorFailure })
    await screen.findAllByText('Failed to load logs')
    expect(calls).toHaveLength(1)
    expect(screen.getByRole('button', { name: 'Refresh latest' })).toBeEnabled()
  }
)

test('shows a requested statistics failure while leaving the current page visible', async () => {
  renderPersonalLogs({ failStats: true })
  await screen.findAllByText('cursor-result')
  await userEvent.click(
    screen.getByRole('button', {
      name: 'Calculate statistics for selected range',
    })
  )
  await screen.findByText('Failed to query statistics')
  expect(screen.queryByText('RPM')).toBeNull()
  expect(screen.getAllByText('cursor-result').length).toBeGreaterThan(0)
})
