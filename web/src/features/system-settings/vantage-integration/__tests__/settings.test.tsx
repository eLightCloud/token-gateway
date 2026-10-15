import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router'
import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
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
import i18next from 'i18next'
import { toast } from 'sonner'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { Route } from '@/routes/_authenticated/system-settings/operations/$section'

import { OperationsSettings } from '../../operations'
import { getOperationsSectionNavItems } from '../../operations/section-registry'
import type {
  VantageIntegrationSettings,
  VantageIntegrationUpdate,
} from '../api'

let settings: VantageIntegrationSettings
let queryClient: QueryClient

async function renderSettings() {
  const root = createRootRoute()
  const authenticated = createRoute({
    getParentRoute: () => root,
    id: '_authenticated',
  })
  const operations = createRoute({
    getParentRoute: () => authenticated,
    path: '/system-settings/operations/$section',
    beforeLoad: Route.options.beforeLoad as (context: {
      params: { section: string }
    }) => void,
    component: OperationsSettings,
  })
  const router = createRouter({
    routeTree: root.addChildren([authenticated.addChildren([operations])]),
    history: createMemoryHistory({
      initialEntries: ['/system-settings/operations/vantage'],
    }),
  })
  render(
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
  await act(() => router.load())
  return router
}

beforeEach(() => {
  vi.spyOn(window, 'scrollTo').mockImplementation(() => undefined)
  queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  settings = {
    enabled: true,
    allowed_ipv4: ['10.0.0.8'],
    secret_configured: true,
  }
  vi.spyOn(api, 'get').mockImplementation(async (url) => {
    if (url === '/api/option/vantage_integration') {
      return { data: { success: true, data: settings } }
    }
    if (url === '/api/option/') return { data: { success: true, data: [] } }
    return { data: { success: true, data: {} } }
  })
  vi.spyOn(api, 'put').mockImplementation(async (_url, body) => {
    const values = body as VantageIntegrationUpdate
    settings = {
      enabled: values.enabled,
      allowed_ipv4: values.allowed_ipv4,
      secret_configured: settings.secret_configured || values.secret !== '',
    }
    return { data: { success: true, data: settings } }
  })
  vi.spyOn(toast, 'success').mockImplementation(() => 'success')
  vi.spyOn(toast, 'error').mockImplementation(() => 'error')
})

afterEach(() => {
  queryClient.clear()
  vi.restoreAllMocks()
})

describe('Vantage integration operations settings', () => {
  it('opens the operations navigation entry with three settings and an empty secret input', async () => {
    const router = await renderSettings()
    expect(
      await screen.findByRole('heading', { name: 'Vantage Integration' })
    ).toBeVisible()
    expect(await screen.findByLabelText('Application Secret')).toHaveValue('')
    expect(
      screen.getByRole('switch', { name: 'Enable Vantage integration' })
    ).toBeChecked()
    expect(screen.getByLabelText('IPv4 Source Allowlist')).toHaveValue(
      '10.0.0.8'
    )
    expect(router.state.location.pathname).toBe(
      '/system-settings/operations/vantage'
    )
    expect(getOperationsSectionNavItems(i18next.t)).toEqual(
      expect.arrayContaining([
        expect.objectContaining({
          title: 'Vantage Integration',
          url: '/system-settings/operations/vantage',
        }),
      ])
    )
    expect(screen.queryByText('TRUSTED_PROXIES')).not.toBeInTheDocument()
  })

  it('deduplicates multiple IPv4 addresses and preserves the key when saving an empty secret', async () => {
    const user = userEvent.setup()
    await renderSettings()
    const allowlist = await screen.findByLabelText('IPv4 Source Allowlist')
    await user.clear(allowlist)
    await user.type(allowlist, '10.0.0.9,10.0.0.10\n10.0.0.9')
    await user.click(screen.getByRole('button', { name: 'Save Changes' }))
    expect(api.put).not.toHaveBeenCalled()
    const confirmation = await screen.findByRole('alertdialog')
    await user.click(
      within(confirmation).getByRole('button', { name: 'Save Changes' })
    )
    await waitFor(() =>
      expect(api.put).toHaveBeenCalledWith('/api/option/vantage_integration', {
        enabled: true,
        allowed_ipv4: ['10.0.0.9', '10.0.0.10'],
        secret: '',
      })
    )
    await waitFor(() =>
      expect(toast.success).toHaveBeenCalledWith(
        'Vantage integration settings saved'
      )
    )
    expect(screen.getByLabelText('IPv4 Source Allowlist')).toHaveValue(
      '10.0.0.9\n10.0.0.10'
    )
  })

  it.each(['::1', '10.0.0.0/24', 'example.com', '300.1.2.3'])(
    'rejects invalid source %s before submitting',
    async (address) => {
      const user = userEvent.setup()
      await renderSettings()
      const allowlist = await screen.findByLabelText('IPv4 Source Allowlist')
      await user.clear(allowlist)
      await user.type(allowlist, address)
      await user.click(screen.getByRole('button', { name: 'Save Changes' }))
      expect(
        await screen.findByText('Only complete IPv4 addresses are allowed.')
      ).toBeVisible()
      expect(api.put).not.toHaveBeenCalled()
    }
  )

  it('requires a key when enabling and clears a generated key after successful save', async () => {
    settings = { enabled: false, allowed_ipv4: [], secret_configured: false }
    const user = userEvent.setup()
    await renderSettings()
    const secret = await screen.findByLabelText('Application Secret')
    await user.click(
      screen.getByRole('switch', { name: 'Enable Vantage integration' })
    )
    await user.type(screen.getByLabelText('IPv4 Source Allowlist'), '10.0.0.8')
    await user.click(screen.getByRole('button', { name: 'Save Changes' }))
    expect(
      await screen.findByText(
        'Set an application secret before enabling integration.'
      )
    ).toBeVisible()
    await user.click(
      screen.getByRole('button', { name: 'Generate application secret' })
    )
    const generated = (secret as HTMLInputElement).value
    expect(generated).toMatch(/^[0-9a-f]{64}$/)
    expect(
      screen.getByRole('button', { name: 'Copy application secret' })
    ).toBeVisible()
    await user.click(screen.getByRole('button', { name: 'Save Changes' }))
    await user.click(
      within(await screen.findByRole('alertdialog')).getByRole('button', {
        name: 'Save Changes',
      })
    )
    await waitFor(() => expect(toast.success).toHaveBeenCalled())
    expect(secret).toHaveValue('')
    expect(
      screen.getByText('An application secret is configured.', { exact: false })
    ).toBeVisible()
    expect(api.put).toHaveBeenCalledWith('/api/option/vantage_integration', {
      enabled: true,
      allowed_ipv4: ['10.0.0.8'],
      secret: generated,
    })
  })

  it('keeps the draft and reports failure when saving fails', async () => {
    vi.mocked(api.put).mockRejectedValueOnce(
      new Error('Settings storage unavailable')
    )
    const user = userEvent.setup()
    await renderSettings()
    const secret = await screen.findByLabelText('Application Secret')
    await user.type(secret, 'new-secret-for-vantage-integration-12345678')
    await user.click(screen.getByRole('button', { name: 'Save Changes' }))
    await user.click(
      within(await screen.findByRole('alertdialog')).getByRole('button', {
        name: 'Save Changes',
      })
    )
    await waitFor(() => expect(toast.error).toHaveBeenCalled())
    expect(toast.success).not.toHaveBeenCalled()
    expect(secret).toHaveValue('new-secret-for-vantage-integration-12345678')
    expect(screen.getByRole('button', { name: 'Save Changes' })).toBeEnabled()
  })

  it('does not save changes when the confirmation is cancelled', async () => {
    const user = userEvent.setup()
    await renderSettings()
    await user.click(
      await screen.findByRole('switch', { name: 'Enable Vantage integration' })
    )
    await user.click(screen.getByRole('button', { name: 'Save Changes' }))
    await user.click(
      within(await screen.findByRole('alertdialog')).getByRole('button', {
        name: 'Cancel',
      })
    )
    expect(api.put).not.toHaveBeenCalled()
  })
  it('shows an error and retries when settings cannot be loaded', async () => {
    const user = userEvent.setup()
    const originalGet = vi.mocked(api.get).getMockImplementation()
    if (!originalGet) throw new Error('Expected the settings request fixture')
    let failed = false
    vi.mocked(api.get).mockImplementation(async (url, config) => {
      if (url === '/api/option/vantage_integration' && !failed) {
        failed = true
        throw new Error('Settings unavailable')
      }
      return originalGet(url, config)
    })
    await renderSettings()
    expect(await screen.findByText('Failed to load settings')).toBeVisible()
    expect(
      screen.queryByLabelText('Application Secret')
    ).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Retry' }))
    expect(await screen.findByLabelText('Application Secret')).toHaveValue('')
  })

  it('rejects a short application secret before submitting', async () => {
    const user = userEvent.setup()
    await renderSettings()
    await user.type(await screen.findByLabelText('Application Secret'), 'short')
    await user.click(screen.getByRole('button', { name: 'Save Changes' }))
    expect(
      await screen.findByText(
        'The application secret must contain at least 32 bytes.'
      )
    ).toBeVisible()
    expect(api.put).not.toHaveBeenCalled()
  })
})
