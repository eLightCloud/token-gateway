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
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  act,
  fireEvent,
  render,
  renderHook,
  screen,
  waitFor,
} from '@testing-library/react'
import { createInstance } from 'i18next'
import { I18nextProvider } from 'react-i18next'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'

import en from '@/i18n/locales/en.json'
import zh from '@/i18n/locales/zh.json'
import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import {
  AdminOrganizationDetailPage,
  OrganizationLogsPage,
  OrganizationUsagePage,
  UsageFilters,
} from '../index'
import { OrganizationInvoicePanel } from '../invoice'
import { useUsageFilters } from '../use-usage-filters'

function FiltersHarness() {
  const filters = useUsageFilters()
  return (
    <UsageFilters
      filters={filters}
      showMemberFilter={false}
      showChannelFilter={false}
      onRefresh={() => {}}
      onExport={() => {}}
    />
  )
}

async function renderRange(element = <FiltersHarness />) {
  const i18n = createInstance()
  await i18n.init({
    lng: 'en',
    resources: { en, zh },
  })
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const view = render(
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={client}>{element}</QueryClientProvider>
    </I18nextProvider>
  )
  return { i18n, client, ...view }
}

describe('organization usage date ranges', () => {
  beforeEach(() => {
    vi.spyOn(Date, 'now').mockReturnValue(Date.parse('2026-10-18T16:30:00Z'))
  })

  afterEach(() => {
    useAuthStore.getState().auth.setUser(null)
    vi.restoreAllMocks()
  })

  test.each([undefined, 1])(
    'invoice scope %s blocks invalid dates and applies a corrected same-day range',
    async (organizationId) => {
      useAuthStore
        .getState()
        .auth.setUser({ id: 1, username: 'tester', role: 100 })
      const get = vi
        .spyOn(api, 'get')
        .mockResolvedValue({ data: { success: true } })
      const view = await renderRange(
        <OrganizationInvoicePanel organizationId={organizationId} />
      )
      await waitFor(() => expect(view.client.isFetching()).toBe(0))
      const start = screen.getByLabelText(/^Start date/)
      const end = screen.getByLabelText(/^End date/)
      expect(start.closest('[data-slot="field-group"]')).toHaveClass(
        'grid',
        'grid-cols-2',
        'w-full',
        'sm:w-96',
        'sm:shrink-0'
      )
      expect(start.closest('[data-slot="card-content"]')).toHaveClass(
        'sm:flex-wrap'
      )
      expect(end).toHaveAttribute('max', '2026-10-19')
      get.mockClear()

      fireEvent.change(start, { target: { value: '2026-11-01' } })
      expect(screen.getByRole('alert')).toBeVisible()
      expect(screen.getByRole('button', { name: 'Query' })).toBeDisabled()
      expect(screen.getByRole('button', { name: 'Query' })).toBeDisabled()
      expect(get).not.toHaveBeenCalled()

      fireEvent.change(start, { target: { value: '2026-10-18' } })
      fireEvent.change(end, { target: { value: '' } })
      expect(screen.getByRole('button', { name: 'Query' })).toBeDisabled()
      fireEvent.change(end, { target: { value: '2026-11-01' } })
      expect(screen.getByRole('button', { name: 'Query' })).toBeDisabled()
      fireEvent.change(end, { target: { value: '2026-10-18' } })
      expect(screen.queryByRole('alert')).not.toBeInTheDocument()
      fireEvent.click(screen.getByRole('button', { name: 'Query' }))
      await waitFor(() =>
        expect(get).toHaveBeenCalledWith(
          `${organizationId ? '/api/admin/organizations/1' : '/api/organization/current'}/invoice?start_date=2026-10-18&end_date=2026-10-18`,
          expect.objectContaining({ signal: expect.any(AbortSignal) })
        )
      )
      view.unmount()
      view.client.clear()
    }
  )

  test('reversed dates show an accessible inline error and disable actions until corrected', async () => {
    await renderRange()
    const start = screen.getByLabelText('Start date (Beijing Time)')
    const end = screen.getByLabelText('End date (Beijing Time)')
    fireEvent.change(end, { target: { value: '2026-10-17' } })
    fireEvent.change(start, { target: { value: '2026-10-18' } })

    expect(screen.getByRole('alert')).toHaveTextContent(
      'Start date cannot be after end date'
    )
    expect(start).toHaveAttribute('aria-invalid', 'true')
    expect(start).toHaveAttribute('max', '2026-10-17')
    expect(end).toHaveAttribute('min', '2026-10-18')
    expect(end).toHaveAccessibleDescription(
      'Start date cannot be after end date'
    )
    expect(screen.getByRole('button', { name: 'Query' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Export' })).toBeDisabled()

    fireEvent.change(end, { target: { value: '2026-10-18' } })
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    expect(start).not.toHaveAttribute('aria-invalid', 'true')
    expect(screen.getByRole('button', { name: 'Query' })).toBeEnabled()
  })

  test('future dates are blocked even when the other boundary is empty, using the Beijing day', async () => {
    await renderRange()
    const start = screen.getByLabelText('Start date (Beijing Time)')
    const end = screen.getByLabelText('End date (Beijing Time)')
    expect(start).toHaveAttribute('max', '2026-10-19')
    expect(end).toHaveAttribute('max', '2026-10-19')
    fireEvent.change(start, { target: { value: '2026-11-01' } })
    expect(screen.getByRole('alert')).toHaveTextContent(
      'Dates cannot be in the future'
    )
    fireEvent.change(start, { target: { value: '' } })
    fireEvent.change(end, { target: { value: '2026-11-01' } })
    expect(screen.getByRole('button', { name: 'Query' })).toBeDisabled()
    fireEvent.change(end, { target: { value: '2026-10-19' } })
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    fireEvent.change(end, { target: { value: '' } })
    expect(screen.getByRole('button', { name: 'Query' })).toBeEnabled()
  })

  test('validation feedback follows the selected interface language', async () => {
    const { i18n } = await renderRange()
    fireEvent.change(screen.getByLabelText('Start date (Beijing Time)'), {
      target: { value: '2026-11-01' },
    })
    await act(() => i18n.changeLanguage('zh'))
    expect(screen.getByRole('alert')).toHaveTextContent('日期不能晚于今天')
  })

  test('malformed calendar dates are rejected instead of becoming an unbounded query', () => {
    const { result } = renderHook(() => useUsageFilters())
    act(() => result.current.setStartDate('2026-02-30'))
    expect(result.current.isDateRangeValid).toBe(false)
    act(() => result.current.setStartDate('2026-02-28'))
    expect(result.current.isDateRangeValid).toBe(true)
  })

  test.each(['admin billing', 'admin logs', 'current billing', 'current logs'])(
    '%s waits for Query after correcting a date range',
    async (page) => {
      const requested: URL[] = []
      vi.spyOn(api, 'get').mockImplementation(async (path) => {
        const url = new URL(path, 'http://localhost')
        if (url.pathname.includes('/billing/')) requested.push(url)
        let data: unknown = { name: 'Test organization', id: 1, status: 1 }
        if (url.pathname === '/api/organization/self') {
          data = { organization: data, member: { role: 'admin' } }
        } else if (url.pathname.includes('/members')) {
          data = []
        } else if (url.pathname.includes('/billing/logs/')) {
          data = { items: [], has_more: false }
        } else if (url.pathname.includes('/billing/usage')) {
          data = { status: 'queued', queue_reason: 'Waiting for data' }
        }
        return { data: { success: true, data } }
      })
      let element = <OrganizationUsagePage />
      if (page.startsWith('admin')) {
        element = <AdminOrganizationDetailPage id={1} />
      }
      if (page === 'current logs') element = <OrganizationLogsPage />
      const view = await renderRange(element)
      if (page.startsWith('admin')) {
        fireEvent.click(
          await screen.findByRole('tab', {
            name: page.endsWith('logs') ? 'Logs' : 'Billing',
          })
        )
      }
      const start = await screen.findByLabelText('Start date (Beijing Time)')
      const end = screen.getByLabelText('End date (Beijing Time)')
      fireEvent.change(end, { target: { value: '2026-10-17' } })
      await waitFor(() => expect(view.client.isFetching()).toBe(0))
      requested.length = 0

      fireEvent.change(start, { target: { value: '2026-10-18' } })
      await act(async () => {})
      expect(requested).toEqual([])
      fireEvent.change(start, { target: { value: '2026-11-01' } })
      fireEvent.change(end, { target: { value: '' } })
      await act(async () => {})
      expect(requested).toEqual([])
      expect(
        screen.queryByRole('button', { name: 'Retry' })
      ).not.toBeInTheDocument()

      fireEvent.change(start, { target: { value: '2026-10-18' } })
      await act(async () => {})
      expect(requested).toEqual([])
      fireEvent.click(screen.getByRole('button', { name: 'Query' }))
      await waitFor(() => expect(requested.length).toBeGreaterThan(0))
      expect(
        requested.every(
          (url) => url.searchParams.get('start_timestamp') === '1792252800'
        )
      ).toBe(true)
      await waitFor(() => expect(view.client.isFetching()).toBe(0))
      requested.length = 0
      fireEvent.change(end, { target: { value: '2026-10-18' } })
      await act(async () => {})
      expect(requested).toEqual([])
      fireEvent.click(screen.getByRole('button', { name: 'Query' }))
      await waitFor(() => expect(requested.length).toBeGreaterThan(0))
      expect(
        requested.every(
          (url) => url.searchParams.get('end_timestamp') === '1792339200'
        )
      ).toBe(true)
      view.unmount()
      view.client.clear()
    }
  )
})
