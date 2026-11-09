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
  createMemoryHistory,
  createRootRoute,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router'
import {
  act,
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18next from 'i18next'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import type { RatioRange } from '@/features/reseller/api'
import zh from '@/i18n/locales/zh.json'
import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'
import { useSystemConfigStore } from '@/stores/system-config-store'

import { OverviewDashboard } from '../overview-dashboard'

const storageKey = 'dashboard_overview_setup_guide_expanded'
let client: QueryClient
let keyLookupError: Error | null
let ratioLookupError: Error | null
let ratioRanges: RatioRange[]
let uptimeEnabled: boolean
let apiInfoEnabled: boolean

beforeEach(() => {
  window.localStorage.clear()
  useSystemConfigStore.setState(useSystemConfigStore.getInitialState(), true)
  useAuthStore.getState().auth.setUser({
    id: 1,
    username: 'dashboard-user',
    role: 1,
    quota: 1000000,
    used_quota: 1000,
    request_count: 1,
  })
  client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  keyLookupError = null
  ratioLookupError = null
  ratioRanges = []
  uptimeEnabled = false
  apiInfoEnabled = false
  vi.spyOn(api, 'get').mockImplementation(async (url) => {
    switch (url) {
      case '/api/token/?p=1&size=10':
        if (keyLookupError) throw keyLookupError
        return {
          data: {
            success: true,
            data: {
              items: [{ id: 1, name: 'App key', key: 'masked', status: 1 }],
            },
          },
        }
      case '/api/status':
        return {
          data: {
            data: {
              api_info_enabled: apiInfoEnabled,
              announcements_enabled: false,
              faq_enabled: false,
              uptime_kuma_enabled: uptimeEnabled,
            },
          },
        }
      case '/api/user/models':
        return { data: { success: true, data: ['gpt-4o-mini'] } }
      case '/api/data/self':
        return { data: { success: true, data: [] } }
      case '/api/reseller/personal/ratios':
        if (ratioLookupError) throw ratioLookupError
        return { data: { success: true, data: ratioRanges } }
      case '/api/uptime/status':
        return { data: { success: true, data: [] } }
      default:
        throw new Error(`Unexpected dashboard request: ${url}`)
    }
  })
})

afterEach(() => {
  cleanup()
  client.clear()
  useAuthStore.setState(useAuthStore.getInitialState(), true)
  useSystemConfigStore.setState(useSystemConfigStore.getInitialState(), true)
  window.localStorage.clear()
})

async function renderOverview() {
  const router = createRouter({
    routeTree: createRootRoute({ component: OverviewDashboard }),
    history: createMemoryHistory({ initialEntries: ['/'] }),
  })
  await router.load()
  return render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
}

describe('overview setup guide', () => {
  it('shows usage first and only a header entry when setup is complete', async () => {
    await renderOverview()

    const toggle = await screen.findByRole('button', { name: 'Setup guide' })
    expect(toggle).toHaveAttribute('aria-expanded', 'false')
    expect(
      screen.getAllByRole('heading').map((heading) => heading.textContent)
    ).toEqual(['Overview', 'Usage at a glance', 'My model discounts'])
    expect(screen.queryByText('Setup guide complete')).not.toBeInTheDocument()
    expect(screen.queryByText('Setup progress: 3/3')).not.toBeInTheDocument()
    for (const name of ['API Keys', 'Channels', 'Usage Logs', 'Pricing']) {
      expect(screen.queryByRole('button', { name })).not.toBeInTheDocument()
    }
    const panel = document.getElementById(
      toggle.getAttribute('aria-controls') ?? ''
    )
    expect(panel).toBeInTheDocument()
    expect(panel).not.toBeVisible()
  })

  it('toggles the completed guide with the keyboard and restores focus after hiding it', async () => {
    const user = userEvent.setup()
    await renderOverview()
    const toggle = await screen.findByRole('button', { name: 'Setup guide' })

    await user.tab()
    expect(toggle).toHaveFocus()
    await user.keyboard('{Enter}')
    expect(toggle).toHaveAttribute('aria-expanded', 'true')
    expect(
      document.getElementById(toggle.getAttribute('aria-controls') ?? '')
    ).toBeVisible()
    expect(
      screen.getByRole('heading', {
        name: 'Build on your API gateway in minutes',
      })
    ).toBeVisible()
    await waitFor(() =>
      expect(screen.getByRole('button', { name: /^API Keys/ })).toBeVisible()
    )

    await user.keyboard(' ')
    expect(toggle).toHaveAttribute('aria-expanded', 'false')
    expect(toggle).toHaveFocus()
    await user.click(toggle)
    await user.click(screen.getByRole('button', { name: 'Hide setup guide' }))
    expect(toggle).toHaveAttribute('aria-expanded', 'false')
    expect(toggle).toHaveFocus()
  })

  it('restores the completed guide preference after remounting', async () => {
    const user = userEvent.setup()
    const first = await renderOverview()
    await user.click(await screen.findByRole('button', { name: 'Setup guide' }))
    first.unmount()

    const second = await renderOverview()
    expect(
      await screen.findByRole('button', { name: 'Setup guide' })
    ).toHaveAttribute('aria-expanded', 'true')
    await user.click(screen.getByRole('button', { name: 'Hide setup guide' }))
    second.unmount()

    await renderOverview()
    expect(
      await screen.findByRole('button', { name: 'Setup guide' })
    ).toHaveAttribute('aria-expanded', 'false')
    expect(screen.queryByText('Setup guide complete')).not.toBeInTheDocument()
  })

  it('keeps the existing progress banner when an incomplete guide is manually collapsed', async () => {
    const user = userEvent.setup()
    useAuthStore
      .getState()
      .auth.setUser({ id: 1, username: 'new-user', role: 1 })
    await renderOverview()

    await user.click(
      await screen.findByRole('button', { name: 'Hide setup guide' })
    )
    expect(screen.getByText('Setup progress: 1/3')).toBeVisible()
    expect(
      screen.getByText('Setup guide is collapsed. Expand it anytime.')
    ).toBeVisible()
    expect(screen.getByRole('button', { name: 'API Keys' })).toBeVisible()
    expect(
      screen.queryByRole('button', { name: 'Setup guide' })
    ).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Show setup guide' }))
    expect(
      screen.getByRole('button', { name: 'Hide setup guide' })
    ).toBeVisible()
  })

  it('removes the collapsed progress banner when the remaining setup step completes', async () => {
    useAuthStore.getState().auth.setUser({
      id: 1,
      username: 'dashboard-user',
      role: 1,
      quota: 1000000,
    })
    window.localStorage.setItem(storageKey, 'collapsed')
    await renderOverview()
    expect(await screen.findByText('Setup progress: 2/3')).toBeVisible()

    act(() => {
      useAuthStore.getState().auth.setUser({
        id: 1,
        username: 'dashboard-user',
        role: 1,
        quota: 1000000,
        request_count: 1,
      })
    })
    expect(
      await screen.findByRole('button', { name: 'Setup guide' })
    ).toHaveAttribute('aria-expanded', 'false')
    expect(screen.queryByText(/Setup progress:/)).not.toBeInTheDocument()
  })

  it('does not show a completed setup entry when the key lookup fails', async () => {
    keyLookupError = new Error('Key lookup unavailable')
    await renderOverview()

    expect(
      await screen.findByRole('button', { name: 'Hide setup guide' })
    ).toBeVisible()
    expect(
      screen.queryByRole('button', { name: 'Setup guide' })
    ).not.toBeInTheDocument()
  })
})

describe('overview model discounts', () => {
  it('shows ten discounted models and expands the same table with the keyboard', async () => {
    const user = userEvent.setup()
    ratioRanges = [
      { model: 'base-price', min: '1', max: '1' },
      { model: 'markup', min: '1.1', max: '1.2' },
      { model: 'mixed-coverage', min: '0.8', max: '1.2' },
      { model: 'discount-range', min: '0.8', max: '0.9' },
      ...Array.from({ length: 9 }, (_, index) => ({
        model: `discounted-${index + 1}`,
        min: '0.85',
        max: '0.85',
      })),
    ]
    await renderOverview()

    const table = within(
      await screen.findByRole('table', { name: 'My model discounts' })
    )
    expect(table.getByRole('columnheader', { name: 'Model' })).toBeVisible()
    expect(
      table.getByRole('columnheader', { name: 'Active discount' })
    ).toBeVisible()
    expect(table.getByText('Up to 20% off')).toBeVisible()
    expect(table.getByText('10–20% off')).toBeVisible()
    expect(table.getAllByRole('row')).toHaveLength(11)
    expect(table.queryByText('discounted-9')).not.toBeInTheDocument()

    const toggle = screen.getByRole('button', { name: 'View all' })
    expect(toggle).toHaveAttribute('aria-expanded', 'false')
    toggle.focus()
    await user.keyboard('{Enter}')
    expect(toggle).toHaveAttribute('aria-expanded', 'true')
    expect(table.getAllByRole('row')).toHaveLength(12)
    expect(table.getByText('discounted-9')).toBeVisible()
    expect(table.queryByText('base-price')).not.toBeInTheDocument()
    expect(table.queryByText('markup')).not.toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Collapse' }))
    expect(table.queryByText('discounted-9')).not.toBeInTheDocument()
  })

  it('keeps uptime below discounts when uptime is enabled', async () => {
    uptimeEnabled = true
    apiInfoEnabled = true
    await renderOverview()

    const discounts = screen.getByRole('heading', {
      name: 'My model discounts',
    })
    const apiInfo = await screen.findByText('API Info')
    const uptime = await screen.findByText('Uptime')
    expect(
      discounts.compareDocumentPosition(uptime) &
        Node.DOCUMENT_POSITION_FOLLOWING
    ).toBeTruthy()
    expect(
      apiInfo.compareDocumentPosition(uptime) & Node.DOCUMENT_POSITION_FOLLOWING
    ).toBeTruthy()
    expect(await screen.findByText('No active model discounts')).toBeVisible()
    expect(
      screen.queryByRole('button', { name: 'View all' })
    ).not.toBeInTheDocument()
  })

  it('shows an error and allows retry when the discount lookup fails', async () => {
    const user = userEvent.setup()
    ratioLookupError = new Error('Discount lookup unavailable')
    await renderOverview()

    const alert = await screen.findByRole('alert')
    expect(
      screen.queryByText('No active model discounts')
    ).not.toBeInTheDocument()
    ratioLookupError = null
    ratioRanges = [{ model: 'my-model', min: '0.8', max: '0.8' }]
    await user.click(within(alert).getByRole('button', { name: 'Retry' }))

    expect(await screen.findByText('my-model')).toBeVisible()
    expect(screen.getByText('20% off')).toBeVisible()
    expect(
      screen.queryByRole('button', { name: 'View all' })
    ).not.toBeInTheDocument()
  })

  it('uses fold notation when the user switches to Simplified Chinese', async () => {
    ratioRanges = [{ model: 'my-model', min: '0.85', max: '0.85' }]
    i18next.addResourceBundle('zhCN', 'translation', zh.translation)
    try {
      await renderOverview()
      expect(await screen.findByText('15% off')).toBeVisible()

      await act(() => i18next.changeLanguage('zhCN'))
      const table = within(screen.getByRole('table', { name: '我的模型折扣' }))
      expect(table.getByText('8.5 折')).toBeVisible()
    } finally {
      await act(() => i18next.changeLanguage('en'))
      i18next.removeResourceBundle('zhCN', 'translation')
    }
  })
})
