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
  createRoute,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router'
import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import { AccountActivation } from '../activation'
import { SecurityCompletionPage } from '../index'

const state = {
  required: true,
  email_verified: false,
  mfa_enabled: false,
  has_password: true,
  complete: false,
  kind: '',
  email: 'alice@example.com',
  activation: '',
}

beforeEach(() => {
  useAuthStore.getState().auth.setUser({ id: 42, username: 'alice', role: 1 })
  vi.spyOn(window, 'scrollTo').mockImplementation(() => undefined)
})
afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
  useAuthStore.getState().auth.reset()
  window.history.replaceState(null, '', '/')
})

async function renderPage(activation = false) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  const root = createRootRoute()
  const route = createRoute({
    getParentRoute: () => root,
    path: activation ? '/account-activation' : '/security-setup',
    component: activation ? AccountActivation : SecurityCompletionPage,
  })
  const router = createRouter({
    routeTree: root.addChildren([route]),
    history: createMemoryHistory({
      initialEntries: [activation ? '/account-activation' : '/security-setup'],
    }),
  })
  await router.load()
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
}

describe('Security enrollment', () => {
  it('restricts an unverified account to security queries and supports keyboard email delivery with cooldown', async () => {
    const get = vi
      .spyOn(api, 'get')
      .mockResolvedValue({ data: { success: true, data: state } })
    const post = vi
      .spyOn(api, 'post')
      .mockResolvedValue({ data: { success: true, data: null } })
    await renderPage()
    const send = await screen.findByRole('button', {
      name: 'Send verification email',
    })
    expect(get.mock.calls.map(([url]) => url)).toEqual([
      '/api/security/completion',
    ])
    expect(
      screen.queryByRole('button', { name: 'Enable' })
    ).not.toBeInTheDocument()
    send.focus()
    await userEvent.keyboard('{Enter}')
    await screen.findByText(
      'Check your email. You can request a new link after one minute.'
    )
    expect(post).toHaveBeenCalledWith(
      '/api/security/email/start',
      {},
      expect.any(Object)
    )
    expect(screen.getByRole('button', { name: /Resend in/ })).toBeDisabled()
  })

  it('shows a recoverable failure without mounting business queries', async () => {
    const get = vi
      .spyOn(api, 'get')
      .mockRejectedValueOnce(new Error('unavailable'))
      .mockResolvedValue({ data: { success: true, data: state } })
    await renderPage()
    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Security status is unavailable'
    )
    await userEvent.click(screen.getByRole('button', { name: 'Retry' }))
    await screen.findByRole('button', { name: 'Send verification email' })
    expect(
      get.mock.calls.every(([url]) => url === '/api/security/completion')
    ).toBe(true)
  })

  it('offers replacement instead of disabling a required authenticator', async () => {
    vi.spyOn(api, 'get').mockImplementation(async (url) => ({
      data: {
        success: true,
        data:
          url === '/api/security/completion'
            ? {
                ...state,
                email_verified: true,
                mfa_enabled: true,
                complete: true,
              }
            : { enabled: true, locked: false, backup_codes_remaining: 8 },
      },
    }))
    await renderPage()
    expect(
      await screen.findByRole('button', { name: 'Replace authenticator' })
    ).toBeEnabled()
    expect(
      screen.queryByRole('button', { name: 'Disable 2FA' })
    ).not.toBeInTheDocument()
  })

  it('removes the email token from the URL and waits for explicit confirmation', async () => {
    window.history.replaceState(
      null,
      '',
      '/account-activation#token=opaque-mail-token&user_id=42&purpose=security_email'
    )
    const post = vi
      .spyOn(api, 'post')
      .mockResolvedValue({ data: { success: true, data: {} } })
    await renderPage(true)
    const confirm = await screen.findByRole('button', { name: 'Verify Email' })
    expect(window.location.hash).toBe('')
    expect(post).not.toHaveBeenCalled()
    await userEvent.click(confirm)
    await waitFor(() =>
      expect(post).toHaveBeenCalledWith(
        '/api/security/email/complete',
        { token: 'opaque-mail-token' },
        expect.objectContaining({
          singleUseAuthorization: true,
          acceptAuthRotation: true,
        })
      )
    )
    expect(await screen.findByRole('status')).toHaveTextContent(
      'Email verified. Continue with security setup.'
    )
  })
})
