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
  focusManager,
  onlineManager,
} from '@tanstack/react-query'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { createInstance } from 'i18next'
import { I18nextProvider } from 'react-i18next'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'

import en from '@/i18n/locales/en.json'
import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import {
  AdminOrganizationDetailPage,
  OrganizationLogsPage,
  OrganizationUsagePage,
} from '../index'
import { OrganizationInvoicePanel } from '../invoice'

const payload = {
  total_quota: 0,
  request_count: 0,
  prompt_tokens: 0,
  completion_tokens: 0,
  member_count: 1,
  active_member_count: 1,
  usage_user_count: 1,
  trend: [],
  members: [
    { user_id: 7, username: 'alice', total_quota: 0, request_count: 1 },
  ],
  models: [{ model_name: 'gpt-4', total_quota: 0, request_count: 1 }],
  channels: [
    { channel_id: 3, channel_name: 'main', total_quota: 0, request_count: 1 },
  ],
  models_truncated: false,
  channels_truncated: false,
  covered_until: 1792252800,
  range_start: 1790784000,
  range_end: 1792252800,
  generated_at: 1792252800,
  calculation_version: 1,
}

type OrganizationResponse = { data: { success: boolean; data: unknown } }

function deferredOrganizationResponse() {
  let resolveResponse: (response: OrganizationResponse) => void
  const promise = new Promise<OrganizationResponse>((resolve) => {
    resolveResponse = resolve
  })
  return {
    promise,
    resolve: (response: OrganizationResponse) => resolveResponse(response),
  }
}

async function renderPage(page: string, initialUsage?: 'building') {
  let initialUsagePending = Boolean(initialUsage)
  const requests: URL[] = []
  const get = vi.spyOn(api, 'get').mockImplementation(async (path) => {
    const url = new URL(path, 'http://localhost')
    if (
      url.pathname.includes('/billing/') ||
      url.pathname.endsWith('/invoice')
    ) {
      requests.push(url)
    }
    let data: unknown = { id: 1, name: 'Test organization', status: 1 }
    if (url.pathname === '/api/organization/self') {
      data = { organization: data, member: { role: 'admin' } }
    } else if (url.pathname.endsWith('/members')) data = []
    else if (url.pathname.includes('/billing/usage')) {
      const building = initialUsagePending
      initialUsagePending = false
      data = {
        status: building ? 'building' : 'ready',
        range_start: payload.range_start,
        snapshot_id: 1,
        revision: 3,
        range_end: payload.range_end,
        payload_generation: 1,
        payload: building ? undefined : payload,
      }
    } else if (url.pathname.includes('/billing/logs/')) {
      data = {
        items: [],
        has_more: true,
        next_cursor: 'page-2',
        range_start: 1790784000,
        range_end: 1792252800,
      }
    } else if (url.pathname.endsWith('/invoice')) {
      data = {
        generation_status: 'ready',
        recalculation_token: 'observed-invoice-generation',
        period: {
          start_date: url.searchParams.get('start_date'),
          end_date: url.searchParams.get('end_date'),
        },
        accounts: [],
        category_rows: [],
        model_rows: [],
        expense_coverage: {
          status: 'complete',
          pending_tasks: 0,
          unknown_rows: 0,
        },
      }
    }
    return { data: { success: true, data } }
  })
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const i18n = createInstance()
  await i18n.init({ lng: 'en', resources: { en } })
  let element = <OrganizationUsagePage />
  if (page.startsWith('admin')) element = <AdminOrganizationDetailPage id={1} />
  else if (page === 'current logs') element = <OrganizationLogsPage />
  else if (page.includes('invoice')) {
    element = (
      <OrganizationInvoicePanel
        organizationId={page === 'invoice admin' ? 1 : undefined}
      />
    )
  }
  const view = render(
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={client}>{element}</QueryClientProvider>
    </I18nextProvider>
  )
  if (page.startsWith('admin')) {
    fireEvent.click(
      await screen.findByRole('tab', {
        name: page.endsWith('logs') ? 'Logs' : 'Billing',
      })
    )
  }
  await screen.findByLabelText(/^Start date/)
  await waitFor(() => expect(client.isFetching()).toBe(0))
  return { requests, get, client, i18n, ...view }
}

describe('manually submitted organization filters', () => {
  beforeEach(() => {
    vi.spyOn(Date, 'now').mockReturnValue(Date.parse('2026-10-18T08:00:00Z'))
    useAuthStore
      .getState()
      .auth.setUser({ id: 1, username: 'tester', role: 100 })
  })
  afterEach(() => {
    vi.useRealTimers()
    vi.restoreAllMocks()
    useAuthStore.getState().auth.setUser(null)
    focusManager.setFocused(undefined)
    onlineManager.setOnline(true)
  })

  test.each([
    ['admin billing', '2026-10-18T16:30:00Z', '2026-10-01', '2026-10-19'],
    ['current billing', '2026-10-18T16:30:00Z', '2026-10-01', '2026-10-19'],
    ['current billing', '2026-12-31T16:30:00Z', '2027-01-01', '2027-01-01'],
  ])(
    '%s initially displays and requests the Beijing month-to-date range at %s',
    async (page, now, startDate, endDate) => {
      vi.mocked(Date.now).mockReturnValue(Date.parse(now))
      const view = await renderPage(page)
      expect(screen.getByLabelText(/^Start date/)).toHaveValue(startDate)
      expect(screen.getByLabelText(/^End date/)).toHaveValue(endDate)
      expect(view.requests).toHaveLength(1)
      expect(view.requests[0].searchParams.get('start_timestamp')).toBe(
        String(Date.parse(`${startDate}T00:00:00+08:00`) / 1000)
      )
      expect(view.requests[0].searchParams.get('end_timestamp')).toBe(
        String(Date.parse(`${endDate}T00:00:00+08:00`) / 1000 + 86400)
      )
      expect(
        screen.queryByText('Filters changed. Click Query to apply.')
      ).not.toBeInTheDocument()
      expect(screen.getByRole('button', { name: 'Export' })).toBeEnabled()
      view.unmount()
      view.client.clear()
    }
  )

  test.each([
    'admin billing',
    'current billing',
    'invoice current',
    'invoice admin',
  ])(
    '%s separates cached Query from confirmed recalculation and preserves the published result',
    async (page) => {
      vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] })
      const view = await renderPage(page)
      const invoice = page.includes('invoice')
      const pending = deferredOrganizationResponse()
      const post = vi
        .spyOn(api, 'post')
        .mockImplementation(() => pending.promise)
      fireEvent.click(screen.getByRole('button', { name: 'Query' }))
      await waitFor(() => expect(view.client.isFetching()).toBe(0))
      expect(
        view.requests.every((url) => !url.searchParams.has('refresh'))
      ).toBe(true)
      expect(post).not.toHaveBeenCalled()
      view.requests.length = 0
      fireEvent.click(
        screen.getByRole('button', { name: 'Force recalculation' })
      )
      await screen.findByRole('alertdialog')
      expect(post).not.toHaveBeenCalled()
      fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
      await waitFor(() =>
        expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
      )
      expect(post).not.toHaveBeenCalled()
      fireEvent.click(
        screen.getByRole('button', { name: 'Force recalculation' })
      )
      fireEvent.click(
        await screen.findByRole('button', { name: 'Confirm recalculation' })
      )
      await waitFor(() => expect(post).toHaveBeenCalledTimes(1))
      const [path, body] = post.mock.calls[0]
      expect(path).toContain(
        invoice ? '/invoice/recalculate?' : '/billing/usage/recalculate?'
      )
      expect(body).toEqual(
        invoice
          ? { expected_token: 'observed-invoice-generation' }
          : { expected_snapshot_id: 1, expected_revision: 3 }
      )
      expect(
        screen.getByRole('button', { name: 'Query', hidden: true })
      ).toBeDisabled()
      fireEvent.click(
        screen.getByRole('button', { name: 'Confirm recalculation' })
      )
      expect(post).toHaveBeenCalledTimes(1)
      await act(async () =>
        pending.resolve({
          data: {
            success: true,
            data: invoice
              ? {
                  generation_status: 'generating',
                  previous_publication: true,
                  period: { start_date: '2026-10-01', end_date: '2026-10-17' },
                  accounts: [],
                  category_rows: [],
                  model_rows: [],
                }
              : {
                  status: 'building',
                  range_start: payload.range_start,
                  range_end: payload.range_end,
                  snapshot_id: 1,
                  revision: 4,
                  payload_generation: 3,
                  payload,
                },
          },
        })
      )
      await waitFor(() =>
        expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
      )
      expect(
        screen.getByText(
          invoice
            ? 'Showing the last published result'
            : 'Showing the last published generation for this range.',
          { exact: false }
        )
      ).toBeVisible()
      expect(view.requests).toEqual([])
      expect(
        screen.getByRole('button', { name: 'Force recalculation' })
      ).toBeDisabled()
      if (!invoice) {
        await act(async () => {
          await vi.advanceTimersByTimeAsync(6000)
        })
        await screen.findByText('Usage trend')
        expect(view.requests).toHaveLength(1)
        expect(view.requests[0].searchParams.get('end_timestamp')).toBe(
          String(payload.range_end)
        )
        expect(view.requests[0].searchParams.get('snapshot_id')).toBe('1')
        expect(post).toHaveBeenCalledTimes(1)
        expect(
          screen.getByRole('button', { name: 'Force recalculation' })
        ).toBeEnabled()
      }
      view.unmount()
      view.client.clear()
    }
  )

  test.each(['admin billing', 'current billing', 'admin logs', 'current logs'])(
    '%s follows only submitted conditions while filters remain drafts',
    async (page) => {
      vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] })
      const view = await renderPage(page)
      const original = view.get.getMockImplementation()
      if (!original) throw new Error('Request fixture is missing')
      let preparing = true
      view.get.mockImplementation(async (...args) => {
        const response = await original(...args)
        if (String(args[0]).includes('/billing/usage') && preparing) {
          return {
            data: {
              success: true,
              data: {
                status: 'building',
                snapshot_id: 7,
                revision: 1,
                range_start: payload.range_start,
                range_end: payload.range_end,
                slice_done: 0,
                slice_total: 1,
                payload,
              },
            },
          }
        }
        return response
      })
      fireEvent.click(screen.getByRole('button', { name: 'Query' }))
      await waitFor(() => expect(view.client.isFetching()).toBe(0))
      view.requests.length = 0
      fireEvent.change(screen.getByLabelText(/^Start date/), {
        target: { value: '2026-10-10' },
      })
      fireEvent.change(screen.getByLabelText(/^End date/), {
        target: { value: '2026-10-12' },
      })
      fireEvent.change(screen.getByRole('combobox', { name: 'User' }), {
        target: { value: '7' },
      })
      fireEvent.change(screen.getByRole('combobox', { name: 'Model' }), {
        target: { value: 'gpt-4' },
      })
      preparing = false
      await act(async () => {
        await vi.advanceTimersByTimeAsync(6000)
      })
      await waitFor(() => expect(view.client.isFetching()).toBe(0))
      expect(view.requests).toHaveLength(1)
      expect(view.requests[0].searchParams.get('start_timestamp')).toBe(
        String(payload.range_start)
      )
      expect(view.requests[0].searchParams.get('end_timestamp')).toBe(
        String(payload.range_end)
      )
      expect(view.requests[0].searchParams.has('user_id')).toBe(false)
      expect(view.requests[0].searchParams.has('model_name')).toBe(false)
      view.requests.length = 0
      fireEvent.click(screen.getByRole('button', { name: 'Query' }))
      await waitFor(() => expect(view.client.isFetching()).toBe(0))
      expect(view.requests).toHaveLength(2)
      expect(
        view.requests
          .find((url) => url.searchParams.has('user_id'))
          ?.searchParams.get('user_id')
      ).toBe('7')
      view.unmount()
      view.client.clear()
    }
  )

  test.each(['current logs', 'current billing'])(
    '%s explicitly retries failed directory preparation',
    async (page) => {
      const view = await renderPage(page)
      if (page.includes('billing')) {
        fireEvent.change(screen.getByRole('combobox', { name: 'Model' }), {
          target: { value: 'gpt-4' },
        })
      }
      const original = view.get.getMockImplementation()
      if (!original) throw new Error('Request fixture is missing')
      let failed = true
      view.get.mockImplementation(async (...args) => {
        const response = await original(...args)
        const url = new URL(String(args[0]), 'http://localhost')
        if (
          url.pathname.endsWith('/billing/usage') &&
          !url.searchParams.has('model_name')
        ) {
          if (failed) {
            return {
              data: {
                success: true,
                data: {
                  status: 'failed',
                  failure_kind: 'capacity',
                  error: 'directory capacity unavailable',
                },
              },
            }
          }
        }
        return response
      })
      fireEvent.click(screen.getByRole('button', { name: 'Query' }))
      await screen.findByText('directory capacity unavailable')
      await waitFor(() => expect(view.client.isFetching()).toBe(0))
      failed = false
      view.requests.length = 0
      fireEvent.click(screen.getAllByRole('button', { name: 'Retry' })[0])
      await waitFor(() =>
        expect(
          screen.queryByText('directory capacity unavailable')
        ).not.toBeInTheDocument()
      )
      const directory = view.requests.find(
        (url) =>
          url.pathname.endsWith('/billing/usage') &&
          !url.searchParams.has('model_name')
      )
      expect(directory?.searchParams.has('refresh')).toBe(false)
      expect(screen.getByRole('combobox', { name: 'Model' })).toBeEnabled()
      view.unmount()
      view.client.clear()
    }
  )

  test.each([
    'admin billing',
    'current billing',
    'admin logs',
    'current logs',
    'invoice current',
    'invoice admin',
  ])(
    '%s ignores cache invalidation and focus changes while editing and waits for Query',
    async (page) => {
      const view = await renderPage(page)
      view.requests.length = 0
      fireEvent.change(screen.getByLabelText(/^Start date/), {
        target: { value: '2026-10-10' },
      })
      await act(async () => {
        await view.client.invalidateQueries()
      })
      act(() => {
        focusManager.setFocused(true)
        onlineManager.setOnline(false)
        onlineManager.setOnline(true)
      })
      await waitFor(() => expect(view.client.isFetching()).toBe(0))
      expect(view.requests).toEqual([])
      fireEvent.click(screen.getByRole('button', { name: 'Query' }))
      await waitFor(() => expect(view.requests.length).toBeGreaterThan(0))
      await waitFor(() => expect(view.client.isFetching()).toBe(0))
      view.unmount()
      view.client.clear()
    }
  )

  test('unmounting the last observer aborts its HTTP read', async () => {
    const view = await renderPage('current billing')
    const pending = deferredOrganizationResponse()
    view.get.mockReturnValueOnce(pending.promise)
    fireEvent.click(screen.getByRole('button', { name: 'Query' }))
    await waitFor(() => expect(view.client.isFetching()).toBe(1))
    const signal = view.get.mock.calls.at(-1)?.[1]?.signal
    expect(signal).toBeInstanceOf(AbortSignal)
    expect(signal?.aborted).toBe(false)
    view.unmount()
    expect(signal?.aborted).toBe(true)
    pending.resolve({
      data: { success: true, data: { status: 'ready', payload } },
    })
    view.client.clear()
  })

  test.each(['admin billing', 'current billing', 'admin logs', 'current logs'])(
    '%s submits all selected filters only on Query and preserves displayed data while editing',
    async (page) => {
      const view = await renderPage(page)
      if (page.endsWith('billing')) expect(view.requests).toHaveLength(1)
      view.requests.length = 0
      fireEvent.change(screen.getByLabelText(/^Start date/), {
        target: { value: '2026-10-10' },
      })
      fireEvent.change(screen.getByLabelText(/^End date/), {
        target: { value: '2026-10-12' },
      })
      fireEvent.change(screen.getByRole('combobox', { name: 'User' }), {
        target: { value: '7' },
      })
      fireEvent.change(screen.getByRole('combobox', { name: 'Model' }), {
        target: { value: 'gpt-4' },
      })
      if (page.endsWith('billing')) {
        fireEvent.change(screen.getByRole('combobox', { name: 'Channel' }), {
          target: { value: '3' },
        })
      }
      await act(async () => {})
      expect(view.requests).toEqual([])
      expect(screen.getByRole('status')).toHaveTextContent(
        'Filters changed. Click Query to apply.'
      )
      expect(screen.getByRole('button', { name: 'Export' })).toBeDisabled()
      fireEvent.click(screen.getByRole('button', { name: 'Query' }))
      await waitFor(() => expect(view.client.isFetching()).toBe(0))
      expect(view.requests).toHaveLength(2)
      expect(
        view.requests.every(
          (url) =>
            url.searchParams.get('start_timestamp') === '1791561600' &&
            url.searchParams.get('end_timestamp') === '1791820800'
        )
      ).toBe(true)
      const result = view.requests.find((url) =>
        url.searchParams.has('user_id')
      )
      expect(result?.searchParams.get('user_id')).toBe('7')
      expect(result?.searchParams.get('model_name')).toBe('gpt-4')
      expect(
        screen.queryByText('Filters changed. Click Query to apply.')
      ).not.toBeInTheDocument()
      view.unmount()
      view.client.clear()
    }
  )

  test.each(['invoice current', 'invoice admin'])(
    '%s presets wait for Query and there is one submit button',
    async (page) => {
      const view = await renderPage(page)
      expect(screen.getByRole('button', { name: 'Export CSV' })).toBeEnabled()
      view.requests.length = 0
      fireEvent.click(screen.getByRole('button', { name: 'Previous month' }))
      await act(async () => {})
      expect(view.requests).toEqual([])
      expect(screen.getByRole('button', { name: 'Export CSV' })).toBeDisabled()
      expect(
        screen.queryByRole('button', { name: 'Apply' })
      ).not.toBeInTheDocument()
      fireEvent.click(screen.getByRole('button', { name: 'Query' }))
      await waitFor(() => expect(view.requests).toHaveLength(1))
      expect(view.requests[0].searchParams.get('start_date')).toBe('2026-09-01')
      expect(view.requests[0].searchParams.get('end_date')).toBe('2026-09-30')
      expect(view.requests[0].searchParams.has('refresh')).toBe(false)
      view.unmount()
      view.client.clear()
    }
  )

  test.each(['admin logs', 'current logs'])(
    '%s preserves the old page while editing and Query requests only the new first page',
    async (page) => {
      const view = await renderPage(page)
      fireEvent.click(screen.getByRole('button', { name: 'Next' }))
      await waitFor(() =>
        expect(
          view.requests.some(
            (url) => url.searchParams.get('cursor') === 'page-2'
          )
        ).toBe(true)
      )
      await waitFor(() => expect(view.client.isFetching()).toBe(0))
      view.requests.length = 0
      fireEvent.change(screen.getByRole('combobox', { name: 'Model' }), {
        target: { value: 'gpt-4' },
      })
      await act(async () => {})
      expect(view.requests).toEqual([])
      expect(screen.getByRole('button', { name: 'Previous' })).toBeEnabled()
      fireEvent.click(screen.getByRole('button', { name: 'Query' }))
      await waitFor(() => expect(view.client.isFetching()).toBe(0))
      const logs = view.requests.filter((url) =>
        url.pathname.includes('/billing/logs/')
      )
      expect(logs).toHaveLength(1)
      expect(logs[0].searchParams.has('cursor')).toBe(false)
      expect(logs[0].searchParams.get('model_name')).toBe('gpt-4')
      view.unmount()
      view.client.clear()
    }
  )

  test.each(['admin logs', 'current logs'])(
    '%s reloads a previously visited continuation after Query even if the server returns the same cursor',
    async (page) => {
      const view = await renderPage(page)
      fireEvent.click(screen.getByRole('button', { name: 'Next' }))
      await waitFor(() => expect(view.client.isFetching()).toBe(0))
      expect(
        view.requests.filter((url) => url.searchParams.has('cursor'))
      ).toHaveLength(1)
      view.requests.length = 0
      fireEvent.click(screen.getByRole('button', { name: 'Previous' }))
      fireEvent.click(screen.getByRole('button', { name: 'Next' }))
      await act(async () => {})
      expect(view.requests).toEqual([])
      fireEvent.click(screen.getByRole('button', { name: 'Query' }))
      await waitFor(() => expect(view.client.isFetching()).toBe(0))
      expect(screen.getByRole('button', { name: 'Previous' })).toBeDisabled()
      view.requests.length = 0
      fireEvent.click(screen.getByRole('button', { name: 'Next' }))
      await waitFor(() =>
        expect(
          view.requests.filter((url) => url.searchParams.has('cursor'))
        ).toHaveLength(1)
      )
      await waitFor(() => expect(view.client.isFetching()).toBe(0))
      view.unmount()
      view.client.clear()
    }
  )

  test('ready billing data does not poll or refetch when the network reconnects', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] })
    const view = await renderPage('current billing')
    view.requests.length = 0
    await act(async () => {
      await vi.advanceTimersByTimeAsync(60000)
    })
    act(() => {
      onlineManager.setOnline(false)
      onlineManager.setOnline(true)
      focusManager.setFocused(true)
    })
    await act(async () => {})
    expect(view.requests).toEqual([])
    view.unmount()
    view.client.clear()
  })

  test('a pending refresh ignores repeated clicks and captures the conditions at submission', async () => {
    const view = await renderPage('current billing')
    const pending = deferredOrganizationResponse()
    view.get.mockImplementationOnce(() => pending.promise)
    view.requests.length = 0
    const start = screen.getByLabelText(/^Start date/)
    fireEvent.change(start, { target: { value: '2026-10-10' } })
    fireEvent.click(screen.getByRole('button', { name: 'Query' }))
    await waitFor(() =>
      expect(screen.getByRole('button', { name: 'Query' })).toBeDisabled()
    )
    fireEvent.click(screen.getByRole('button', { name: 'Query' }))
    fireEvent.change(start, { target: { value: '2026-10-11' } })
    await act(async () =>
      pending.resolve({
        data: { success: true, data: { status: 'ready', payload } },
      })
    )
    await waitFor(() => expect(view.client.isFetching()).toBe(0))
    // The intercepted request does not go through the fixture's recording callback.
    expect(
      view.get.mock.calls.filter(([path]) =>
        path.includes('start_timestamp=1791561600')
      )
    ).toHaveLength(1)
    expect(
      view.get.mock.calls.some(([path]) =>
        path.includes('start_timestamp=1791648000')
      )
    ).toBe(false)
    expect(start).toHaveValue('2026-10-11')
    expect(screen.getByRole('status')).toHaveTextContent('Filters changed.')
    view.unmount()
    view.client.clear()
  })

  test('Retry uses submitted conditions after a failed refresh even while other conditions are being edited', async () => {
    const view = await renderPage('current billing')
    view.get.mockRejectedValueOnce(new Error('service unavailable'))
    fireEvent.click(screen.getByRole('button', { name: 'Query' }))
    await screen.findAllByText('service unavailable')
    await waitFor(() => expect(view.client.isFetching()).toBe(0))
    fireEvent.change(screen.getByRole('combobox', { name: 'Model' }), {
      target: { value: 'gpt-4' },
    })
    view.requests.length = 0
    fireEvent.click(screen.getAllByRole('button', { name: 'Retry' })[0])
    await waitFor(() => expect(view.requests).toHaveLength(1))
    expect(view.requests[0].searchParams.has('model_name')).toBe(false)
    expect(screen.getByRole('status')).toHaveTextContent('Filters changed.')
    view.unmount()
    view.client.clear()
  })

  test('billing and log tabs keep independent drafts without querying on tab return', async () => {
    const view = await renderPage('admin billing')
    fireEvent.change(screen.getByLabelText(/^Start date/), {
      target: { value: '2026-10-10' },
    })
    view.requests.length = 0
    fireEvent.click(screen.getByRole('tab', { name: 'Logs' }))
    await waitFor(() => expect(view.client.isFetching()).toBe(0))
    expect(screen.getByLabelText(/^Start date/)).toHaveValue('')
    expect(view.requests).toHaveLength(2)
    expect(
      view.requests.every((url) => !url.searchParams.has('start_timestamp'))
    ).toBe(true)
    view.requests.length = 0
    fireEvent.click(screen.getByRole('tab', { name: 'Billing' }))
    await act(async () => {})
    expect(screen.getByLabelText(/^Start date/)).toHaveValue('2026-10-10')
    expect(view.requests).toEqual([])
    view.unmount()
    view.client.clear()
  })

  test('an old organization response cannot replace the new organization result or filters', async () => {
    const view = await renderPage('admin billing')
    const pending = deferredOrganizationResponse()
    view.get.mockImplementationOnce(() => pending.promise)
    fireEvent.change(screen.getByLabelText(/^Start date/), {
      target: { value: '2026-10-10' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Query' }))
    await waitFor(() => expect(view.client.isFetching()).toBe(1))
    view.rerender(
      <I18nextProvider i18n={view.i18n}>
        <QueryClientProvider client={view.client}>
          <AdminOrganizationDetailPage id={2} />
        </QueryClientProvider>
      </I18nextProvider>
    )
    fireEvent.click(await screen.findByRole('tab', { name: 'Billing' }))
    await screen.findByRole('combobox', { name: 'Model' })
    await act(async () =>
      pending.resolve({
        data: {
          success: true,
          data: {
            status: 'ready',
            payload: {
              ...payload,
              members: [
                {
                  user_id: 99,
                  username: 'old-organization-member',
                  total_quota: 0,
                },
              ],
            },
          },
        },
      })
    )
    await waitFor(() => expect(view.client.isFetching()).toBe(0))
    expect(screen.getByLabelText(/^Start date/)).toHaveValue('2026-10-01')
    expect(screen.getByLabelText(/^End date/)).toHaveValue('2026-10-18')
    expect(
      screen.queryByText('Filters changed. Click Query to apply.')
    ).not.toBeInTheDocument()
    expect(
      screen.queryByText('old-organization-member')
    ).not.toBeInTheDocument()
    view.unmount()
    view.client.clear()
  })

  test('switching organizations cancels the old automatic target and ignores its late result', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] })
    const view = await renderPage('admin billing')
    view.get.mockResolvedValueOnce({
      data: {
        success: true,
        data: {
          status: 'building',
          snapshot_id: 7,
          revision: 1,
          range_start: payload.range_start,
          range_end: payload.range_end,
          slice_done: 0,
          slice_total: 1,
        },
      },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Query' }))
    await waitFor(() => expect(view.client.isFetching()).toBe(0))
    const pending = deferredOrganizationResponse()
    view.get.mockReturnValueOnce(pending.promise)
    await act(async () => {
      await vi.advanceTimersByTimeAsync(6000)
    })
    await waitFor(() => expect(view.client.isFetching()).toBe(1))
    const signal = view.get.mock.calls.at(-1)?.[1]?.signal
    view.rerender(
      <I18nextProvider i18n={view.i18n}>
        <QueryClientProvider client={view.client}>
          <AdminOrganizationDetailPage id={2} />
        </QueryClientProvider>
      </I18nextProvider>
    )
    fireEvent.click(await screen.findByRole('tab', { name: 'Billing' }))
    await waitFor(() => expect(view.client.isFetching()).toBe(0))
    expect(signal?.aborted).toBe(true)
    await act(async () => {
      pending.resolve({
        data: {
          success: true,
          data: {
            status: 'ready',
            payload: {
              ...payload,
              members: [
                {
                  user_id: 99,
                  username: 'old-tracking-member',
                  total_quota: 0,
                },
              ],
            },
          },
        },
      })
      await vi.advanceTimersByTimeAsync(60000)
    })
    expect(screen.queryByText('old-tracking-member')).not.toBeInTheDocument()
    expect(screen.getByRole('combobox', { name: 'Model' })).toBeEnabled()
    view.unmount()
    view.client.clear()
  })

  test.each([
    ['current billing', 'building'],
    ['admin billing', 'building'],
    ['current billing', 'queued'],
    ['admin billing', 'queued'],
  ] as const)(
    '%s follows a %s target automatically, pins its cutoff and stops when ready',
    async (page, status) => {
      vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] })
      const view = await renderPage(page)
      view.get.mockResolvedValueOnce({
        data: {
          success: true,
          data: {
            status,
            snapshot_id: status === 'building' ? 7 : 0,
            revision: 1,
            range_start: 1788278400,
            range_end: payload.range_end,
            slice_done: 0,
            slice_total: 1,
          },
        },
      })
      fireEvent.change(screen.getByLabelText(/^Start date/), {
        target: { value: '2026-09-02' },
      })
      fireEvent.click(screen.getByRole('button', { name: 'Query' }))
      await waitFor(() => expect(view.client.isFetching()).toBe(0))
      expect(screen.queryByText('Usage trend')).not.toBeInTheDocument()
      expect(screen.getByRole('combobox', { name: 'Model' })).toBeDisabled()
      view.requests.length = 0
      vi.mocked(Date.now).mockReturnValue(Date.parse('2026-10-18T09:00:00Z'))
      await act(async () => {
        await vi.advanceTimersByTimeAsync(6000)
      })
      await screen.findByText('Usage trend')
      expect(view.requests).toHaveLength(1)
      expect(view.requests[0].searchParams.get('start_timestamp')).toBe(
        '1788278400'
      )
      expect(view.requests[0].searchParams.get('end_timestamp')).toBe(
        String(payload.range_end)
      )
      expect(view.requests[0].searchParams.get('snapshot_id')).toBe(
        status === 'building' ? '7' : null
      )
      expect(view.requests[0].searchParams.has('refresh')).toBe(false)
      expect(screen.getByRole('combobox', { name: 'Model' })).toBeEnabled()
      await act(async () => {
        await vi.advanceTimersByTimeAsync(60000)
      })
      expect(view.requests).toHaveLength(1)
      view.unmount()
      view.client.clear()
    }
  )

  test('the initial page load follows a pending snapshot without a Query click', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] })
    const view = await renderPage('current billing', 'building')
    expect(screen.queryByText('Usage trend')).not.toBeInTheDocument()
    await act(async () => {
      await vi.advanceTimersByTimeAsync(6000)
    })
    await screen.findByText('Usage trend')
    expect(view.requests).toHaveLength(2)
    expect(view.requests[1].searchParams.get('end_timestamp')).toBe(
      String(payload.range_end)
    )
    await act(async () => {
      await vi.advanceTimersByTimeAsync(60000)
    })
    expect(view.requests).toHaveLength(2)
    view.unmount()
    view.client.clear()
  })

  test('filtered data and its unfiltered directory each finish without a second click', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] })
    const view = await renderPage('current billing')
    const original = view.get.getMockImplementation()
    if (!original) throw new Error('Request fixture is missing')
    const pendingScopes = new Set(['gpt-4', ''])
    view.get.mockImplementation(async (...args) => {
      const response = await original(...args)
      const url = new URL(String(args[0]), 'http://localhost')
      const scope = url.searchParams.get('model_name') ?? ''
      if (
        url.pathname.endsWith('/billing/usage') &&
        pendingScopes.delete(scope)
      ) {
        return {
          data: {
            success: true,
            data: {
              status: 'building',
              snapshot_id: scope ? 8 : 9,
              revision: 1,
              range_start: payload.range_start,
              range_end: payload.range_end,
              slice_done: 0,
              slice_total: 1,
            },
          },
        }
      }
      return response
    })
    fireEvent.change(screen.getByRole('combobox', { name: 'Model' }), {
      target: { value: 'gpt-4' },
    })
    view.requests.length = 0
    fireEvent.click(screen.getByRole('button', { name: 'Query' }))
    await waitFor(() => expect(view.client.isFetching()).toBe(0))
    expect(view.requests).toHaveLength(2)
    await act(async () => {
      await vi.advanceTimersByTimeAsync(6000)
    })
    await screen.findByText('Usage trend')
    expect(screen.getByRole('combobox', { name: 'Model' })).toBeEnabled()
    expect(view.requests).toHaveLength(4)
    const follows = view.requests.slice(2)
    expect(
      follows.filter((url) => url.searchParams.has('model_name'))
    ).toHaveLength(1)
    expect(
      follows.every(
        (url) =>
          url.searchParams.get('end_timestamp') === String(payload.range_end)
      )
    ).toBe(true)
    expect(follows.every((url) => !url.searchParams.has('refresh'))).toBe(true)
    view.unmount()
    view.client.clear()
  })

  test('a hidden billing tab stops tracking and resumes its submitted target on return', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] })
    const view = await renderPage('admin billing')
    view.get.mockResolvedValueOnce({
      data: {
        success: true,
        data: {
          status: 'building',
          snapshot_id: 7,
          revision: 1,
          range_start: payload.range_start,
          range_end: payload.range_end,
          slice_done: 0,
          slice_total: 1,
        },
      },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Query' }))
    await waitFor(() => expect(view.client.isFetching()).toBe(0))
    fireEvent.click(screen.getByRole('tab', { name: 'Logs' }))
    await waitFor(() => expect(view.client.isFetching()).toBe(0))
    view.requests.length = 0
    await act(async () => {
      await vi.advanceTimersByTimeAsync(60000)
    })
    expect(view.requests).toEqual([])
    fireEvent.click(screen.getByRole('tab', { name: 'Billing' }))
    await act(async () => {
      await vi.advanceTimersByTimeAsync(6000)
    })
    await screen.findByText('Usage trend')
    expect(view.requests).toHaveLength(1)
    expect(view.requests[0].searchParams.get('snapshot_id')).toBe('7')
    expect(view.requests[0].searchParams.get('end_timestamp')).toBe(
      String(payload.range_end)
    )
    view.unmount()
    view.client.clear()
  })

  test('tracking uses the pending target cutoff instead of the previous published payload cutoff', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] })
    const view = await renderPage('current billing')
    const target = payload.range_end + 300
    view.get.mockResolvedValueOnce({
      data: {
        success: true,
        data: {
          status: 'building',
          snapshot_id: 7,
          revision: 2,
          range_start: payload.range_start,
          range_end: target,
          slice_done: 0,
          slice_total: 1,
          payload,
        },
      },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Query' }))
    await waitFor(() => expect(view.client.isFetching()).toBe(0))
    view.requests.length = 0
    await act(async () => {
      await vi.advanceTimersByTimeAsync(6000)
    })
    expect(view.requests).toHaveLength(1)
    expect(view.requests[0].searchParams.get('end_timestamp')).toBe(
      String(target)
    )
    view.unmount()
    view.client.clear()
  })

  test('an empty waiting bucket does not create an endless task tracker', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] })
    const view = await renderPage('current billing')
    view.get.mockResolvedValueOnce({
      data: {
        success: true,
        data: {
          status: 'queued',
          snapshot_id: 0,
          revision: 0,
          range_start: payload.range_end,
          range_end: payload.range_end,
          slice_done: 0,
          slice_total: 0,
          queue_reason: 'waiting for the first completed time bucket',
        },
      },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Query' }))
    await waitFor(() => expect(view.client.isFetching()).toBe(0))
    view.requests.length = 0
    await act(async () => {
      await vi.advanceTimersByTimeAsync(60000)
    })
    expect(view.requests).toEqual([])
    view.unmount()
    view.client.clear()
  })

  test.each(['failed', 'paused', 'invalidated'] as const)(
    'automatic tracking stops on %s',
    async (status) => {
      vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] })
      const view = await renderPage('current billing')
      view.get.mockResolvedValueOnce({
        data: {
          success: true,
          data: {
            status,
            snapshot_id: 7,
            revision: 1,
            range_start: payload.range_start,
            range_end: payload.range_end,
            slice_done: 0,
            slice_total: 1,
          },
        },
      })
      fireEvent.click(screen.getByRole('button', { name: 'Query' }))
      await waitFor(() => expect(view.client.isFetching()).toBe(0))
      view.requests.length = 0
      await act(async () => {
        await vi.advanceTimersByTimeAsync(60000)
      })
      expect(view.requests).toEqual([])
      view.unmount()
      view.client.clear()
    }
  )

  test('tracking stops after a network failure and Retry resumes the submitted target', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] })
    const view = await renderPage('current billing')
    view.get.mockResolvedValueOnce({
      data: {
        success: true,
        data: {
          status: 'building',
          snapshot_id: 7,
          revision: 1,
          range_start: payload.range_start,
          range_end: payload.range_end,
          slice_done: 0,
          slice_total: 1,
        },
      },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Query' }))
    await waitFor(() => expect(view.client.isFetching()).toBe(0))
    view.get.mockRejectedValueOnce(new Error('tracking unavailable'))
    const calls = view.get.mock.calls.length
    await act(async () => {
      await vi.advanceTimersByTimeAsync(6000)
    })
    await screen.findAllByText('tracking unavailable')
    await act(async () => {
      await vi.advanceTimersByTimeAsync(60000)
    })
    expect(view.get.mock.calls).toHaveLength(calls + 1)
    fireEvent.click(screen.getAllByRole('button', { name: 'Retry' })[0])
    await screen.findByText('Usage trend')
    expect(view.get.mock.calls.at(-1)?.[0]).toContain(
      `end_timestamp=${payload.range_end}`
    )
    view.unmount()
    view.client.clear()
  })

  test('leaving the page aborts an automatic follow-up and cancels further tracking', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] })
    const view = await renderPage('current billing')
    view.get.mockResolvedValueOnce({
      data: {
        success: true,
        data: {
          status: 'building',
          snapshot_id: 7,
          revision: 1,
          range_start: payload.range_start,
          range_end: payload.range_end,
          slice_done: 0,
          slice_total: 1,
        },
      },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Query' }))
    await waitFor(() => expect(view.client.isFetching()).toBe(0))
    const pending = deferredOrganizationResponse()
    view.get.mockReturnValueOnce(pending.promise)
    await act(async () => {
      await vi.advanceTimersByTimeAsync(6000)
    })
    await waitFor(() => expect(view.client.isFetching()).toBe(1))
    const signal = view.get.mock.calls.at(-1)?.[1]?.signal
    expect(signal?.aborted).toBe(false)
    view.unmount()
    expect(signal?.aborted).toBe(true)
    const calls = view.get.mock.calls.length
    await act(async () => {
      pending.resolve({
        data: { success: true, data: { status: 'ready', payload } },
      })
      await vi.advanceTimersByTimeAsync(60000)
    })
    expect(view.get.mock.calls).toHaveLength(calls)
    view.client.clear()
  })

  test.each(['invoice current', 'invoice admin'])(
    '%s reads generation progress only on Query without restarting the build',
    async (page) => {
      vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] })
      const view = await renderPage(page)
      view.get.mockResolvedValueOnce({
        data: {
          success: true,
          data: {
            generation_status: 'generating',
            period: { start_date: '2026-10-01', end_date: '2026-10-17' },
          },
        },
      })
      fireEvent.click(screen.getByRole('button', { name: 'Query' }))
      await screen.findByText('Generating...')
      await waitFor(() => expect(view.client.isFetching()).toBe(0))
      view.requests.length = 0
      fireEvent.click(screen.getByRole('button', { name: 'Previous month' }))
      await act(async () => {
        await vi.advanceTimersByTimeAsync(60000)
      })
      expect(view.requests).toEqual([])
      fireEvent.click(screen.getByRole('button', { name: 'Current month' }))
      fireEvent.click(screen.getByRole('button', { name: 'Query' }))
      await waitFor(() =>
        expect(screen.queryByText('Generating...')).not.toBeInTheDocument()
      )
      expect(view.requests).toHaveLength(1)
      expect(view.requests[0].searchParams.get('start_date')).toBe('2026-10-01')
      expect(view.requests[0].searchParams.has('refresh')).toBe(false)
      view.unmount()
      view.client.clear()
    }
  )

  test('returning to the invoice tab preserves its submitted period and pending edits without querying', async () => {
    const view = await renderPage('admin billing')
    fireEvent.click(screen.getByRole('tab', { name: 'Invoice' }))
    await waitFor(() => expect(view.client.isFetching()).toBe(0))
    fireEvent.click(screen.getByRole('button', { name: 'Previous month' }))
    fireEvent.click(screen.getByRole('button', { name: 'Query' }))
    await waitFor(() => expect(view.client.isFetching()).toBe(0))
    fireEvent.change(screen.getByLabelText(/^Start date/), {
      target: { value: '2026-09-10' },
    })
    fireEvent.click(screen.getByRole('tab', { name: 'Billing' }))
    await waitFor(() => expect(view.client.isFetching()).toBe(0))
    view.requests.length = 0
    fireEvent.click(screen.getByRole('tab', { name: 'Invoice' }))
    await act(async () => {})
    expect(screen.getByLabelText(/^Start date/)).toHaveValue('2026-09-10')
    expect(screen.getByText('2026-09-01 — 2026-09-30')).toBeVisible()
    expect(view.requests).toEqual([])
    view.unmount()
    view.client.clear()
  })

  test('Query reloads the option directory and preserves a selected model missing from the updated directory', async () => {
    const view = await renderPage('current billing')
    fireEvent.change(screen.getByRole('combobox', { name: 'Model' }), {
      target: { value: 'gpt-4' },
    })
    view.requests.length = 0
    view.get.mockImplementation(async (path) => {
      const url = new URL(path, 'http://localhost')
      view.requests.push(url)
      const directoryPayload = {
        ...payload,
        models: [{ model_name: 'gpt-5', total_quota: 0, request_count: 1 }],
      }
      return {
        data: {
          success: true,
          data: {
            status: 'ready',
            payload: url.searchParams.has('model_name')
              ? payload
              : directoryPayload,
          },
        },
      }
    })
    fireEvent.click(screen.getByRole('button', { name: 'Query' }))
    await waitFor(() =>
      expect(screen.getByRole('option', { name: 'gpt-5' })).toBeInTheDocument()
    )
    expect(view.requests).toHaveLength(2)
    expect(screen.getByRole('combobox', { name: 'Model' })).toHaveValue('gpt-4')
    view.unmount()
    view.client.clear()
  })

  test('Query stays locked until the entire request round settles when one request fails', async () => {
    const view = await renderPage('current billing')
    const directory = deferredOrganizationResponse()
    view.get.mockRejectedValueOnce(new Error('statistics unavailable'))
    view.get.mockImplementationOnce(() => directory.promise)
    fireEvent.change(screen.getByLabelText(/^Start date/), {
      target: { value: '2026-10-10' },
    })
    fireEvent.change(screen.getByRole('combobox', { name: 'Model' }), {
      target: { value: 'gpt-4' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Query' }))
    await screen.findByText('statistics unavailable')
    expect(screen.getByRole('button', { name: 'Query' })).toBeDisabled()
    await act(async () =>
      directory.resolve({
        data: { success: true, data: { status: 'ready', payload } },
      })
    )
    await waitFor(() =>
      expect(screen.getByRole('button', { name: 'Query' })).toBeEnabled()
    )
    view.unmount()
    view.client.clear()
  })
})
