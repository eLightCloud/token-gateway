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
import type { InternalAxiosRequestConfig } from 'axios'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import * as passwordEncryption from '@/features/auth/lib/password-encryption'
import { api } from '@/lib/api'
import { Route as SignInRoute } from '@/routes/(auth)/sign-in'
import { useAuthStore } from '@/stores/auth-store'

import { AccountActivation } from '../activation'
import { SecurityCompletionPage } from '../index'

vi.mock('@/features/auth/sign-in', () => ({
  SignIn: () => <h1>Sign in</h1>,
}))

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
  const signIn = createRoute({
    getParentRoute: () => root,
    path: '/sign-in',
    validateSearch: SignInRoute.options.validateSearch,
    beforeLoad: SignInRoute.options.beforeLoad as (context: {
      search: { redirect?: string; passwordSetupComplete?: boolean }
    }) => Promise<void>,
    component: SignInRoute.options.component,
  })
  const router = createRouter({
    routeTree: root.addChildren([route, signIn]),
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

function openPasswordLink(purpose = 'account_activation') {
  window.history.replaceState(
    null,
    '',
    `/account-activation#token=opaque-mail-token&user_id=42&purpose=${purpose}`
  )
}

describe('Email password setup', () => {
  it.each([
    ['', 'Please confirm your password'],
    ['different password', 'Passwords do not match'],
  ])(
    'blocks confirmation for an invalid repeated password (%s)',
    async (repeat, message) => {
      openPasswordLink()
      const post = vi.spyOn(api, 'post')
      const user = userEvent.setup()
      await renderPage(true)
      const password = await screen.findByLabelText('Password', { exact: true })
      const confirmation = screen.getByLabelText('Confirm password')
      expect(password).toHaveAttribute('autocomplete', 'new-password')
      expect(confirmation).toHaveAttribute('autocomplete', 'new-password')
      await user.type(password, 'a valid test password')
      if (repeat) await user.type(confirmation, repeat)
      await user.click(screen.getByRole('button', { name: 'Confirm' }))
      expect(await screen.findByText(message)).toBeVisible()
      expect(confirmation).toHaveAttribute('aria-invalid', 'true')
      expect(confirmation).toHaveFocus()
      expect(post).not.toHaveBeenCalled()
    }
  )

  it.each([
    ['account_activation', '/api/security/activation', true],
    ['security_password_reset', '/api/security/password/reset', false],
  ])(
    'opens sign-in after successfully completing %s',
    async (purpose, endpoint, alreadySignedIn) => {
      const auth = useAuthStore.getState().auth
      if (alreadySignedIn) {
        auth.setUser({ id: 7, username: 'operator', role: 10 })
      } else {
        auth.reset()
      }
      auth.setBootstrapState('complete')
      const existingUser = useAuthStore.getState().auth.user
      openPasswordLink(purpose)
      vi.spyOn(passwordEncryption, 'encryptPassword').mockResolvedValue({
        password_encrypted: 'encrypted-password',
        encryption_key_id: 'test-key',
      })
      const response = {
        data: { success: true, data: { notification_warning: true } },
      }
      let finishRequest!: (value: typeof response) => void
      const pendingResponse = new Promise<typeof response>((resolve) => {
        finishRequest = resolve
      })
      const post = vi.spyOn(api, 'post').mockReturnValue(pendingResponse)
      const user = userEvent.setup()
      await renderPage(true)
      const password = await screen.findByLabelText('Password', { exact: true })
      expect(
        screen.queryByRole('link', { name: 'Sign In' })
      ).not.toBeInTheDocument()
      expect(window.location.hash).toBe('')
      expect(post).not.toHaveBeenCalled()
      await user.type(password, 'a valid test password')
      const confirmation = screen.getByLabelText('Confirm password')
      const confirm = screen.getByRole('button', { name: 'Confirm' })
      await user.type(confirmation, 'a valid test password{Enter}')
      await waitFor(() => expect(confirm).toBeDisabled())
      expect(password).toBeDisabled()
      expect(confirmation).toBeDisabled()
      expect(
        screen.queryByRole('heading', { name: 'Sign in' })
      ).not.toBeInTheDocument()
      finishRequest(response)
      expect(
        await screen.findByRole('heading', { name: 'Sign in' })
      ).toBeVisible()
      expect(post.mock.calls).toEqual([
        [
          endpoint,
          {
            token: 'opaque-mail-token',
            user_id: 42,
            purpose,
            password_encrypted: 'encrypted-password',
            encryption_key_id: 'test-key',
          },
          expect.objectContaining({ skipAuthRefresh: true }),
        ],
      ])
      expect(useAuthStore.getState().auth.user).toBe(existingUser)
      expect(useAuthStore.getState().auth.accessToken).toBeNull()
    }
  )

  it('stays on the form if the server rejects an expired or already used link', async () => {
    openPasswordLink()
    vi.spyOn(passwordEncryption, 'encryptPassword').mockResolvedValue({
      password_encrypted: 'encrypted-password',
      encryption_key_id: 'test-key',
    })
    vi.spyOn(api, 'post').mockResolvedValue({
      data: { success: false, code: 'AUTH_FLOW_INVALID' },
    })
    const user = userEvent.setup()
    await renderPage(true)
    await user.type(
      await screen.findByLabelText('Password', { exact: true }),
      'a valid test password'
    )
    await user.type(
      screen.getByLabelText('Confirm password'),
      'a valid test password'
    )
    await user.click(screen.getByRole('button', { name: 'Confirm' }))
    expect(await screen.findByRole('alert')).toHaveTextContent(
      'This link is invalid or has expired. Request a new link.'
    )
    expect(
      screen.queryByRole('heading', { name: 'Sign in' })
    ).not.toBeInTheDocument()
  })
})

it('allows anonymous email-change confirmation without requiring or refreshing a dashboard session', async () => {
  useAuthStore.getState().auth.reset()
  window.history.replaceState(
    null,
    '',
    '/account-activation#token=opaque-mail-token&user_id=42&purpose=security_email_change'
  )
  const previousAdapter = api.defaults.adapter
  const requests: string[] = []
  api.defaults.adapter = async (config: InternalAxiosRequestConfig) => {
    requests.push(config.url ?? '')
    return {
      config,
      headers: {},
      status: 200,
      statusText: 'OK',
      data: { success: true, data: {} },
    }
  }
  try {
    await renderPage(true)
    await userEvent.click(
      await screen.findByRole('button', { name: 'Verify Email' })
    )
    expect(await screen.findByRole('status')).toHaveTextContent(
      'Email verified. Continue with security setup.'
    )
    expect(requests).toEqual(['/api/security/email-change/complete'])
    expect(useAuthStore.getState().auth.accessToken).toBeNull()
  } finally {
    api.defaults.adapter = previousAdapter
  }
})

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
