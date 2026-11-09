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
import { act, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18next from 'i18next'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import zh from '@/i18n/locales/zh.json'
import { api } from '@/lib/api'

import { EmailDeliverySettingsSection } from '../settings-section'
import type { EmailSettings, EmailUpdate } from '../types'

const { verify } = vi.hoisted(() => ({ verify: vi.fn() }))
vi.mock('@/features/auth/secure-verification', () => ({
  SecureVerificationDialog: () => null,
  useSecureVerification: () => ({
    requestVerification: verify,
    dialogProps: {},
  }),
}))
let settings: EmailSettings
let client: QueryClient
async function renderSettings() {
  const root = createRootRoute({ component: EmailDeliverySettingsSection })
  const router = createRouter({
    routeTree: root,
    history: createMemoryHistory({ initialEntries: ['/'] }),
  })
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
  await act(() => router.load())
  await screen.findByLabelText('Authentication method')
}
beforeEach(async () => {
  i18next.addResourceBundle('zh', 'translation', zh.translation, true, true)
  await i18next.changeLanguage('en')
  client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  settings = {
    revision: 4,
    options: {
      auth_mode: 'microsoft_oauth2_app',
      legacy: {
        host: 'smtp.example.com',
        port: 587,
        account: 'legacy@example.com',
        from: 'legacy@example.com',
        ssl: false,
        starttls: true,
        insecure_skip_verify: false,
        force_login: false,
      },
      microsoft: {
        tenant_id: '11111111-1111-1111-1111-111111111111',
        client_id: '22222222-2222-2222-2222-222222222222',
        mailbox: 'sender@example.com',
      },
    },
    legacy_password_configured: true,
    client_secret_configured: true,
    storage_key_ready: true,
  }
  verify.mockResolvedValue({ proof_token: 'scoped-proof' })
  vi.spyOn(api, 'get').mockImplementation(async () => ({
    data: { success: true, data: settings },
  }))
  vi.spyOn(api, 'put').mockImplementation(async (_url, body) => {
    const update = body as EmailUpdate
    settings = {
      ...settings,
      options: update.intent.options,
      revision: settings.revision + 1,
    }
    return { data: { success: true, data: settings } }
  })
  vi.spyOn(api, 'post').mockResolvedValue({
    data: {
      success: true,
      data: {
        revision: 4,
        timestamp: 1793880000,
        request_id: 'email-test-request',
        code: 'EMAIL_ACCEPTED',
        stages: [
          { name: 'config', status: 'passed' },
          { name: 'graph_submit', status: 'passed' },
        ],
      },
    },
  })
})
afterEach(() => {
  client.clear()
  vi.restoreAllMocks()
})

describe('system email settings', () => {
  it('saves the entire configuration once and keeps blank credentials without exposing stored values', async () => {
    await renderSettings()
    expect(
      screen.getByRole('option', {
        name: 'Microsoft 365 (Microsoft Graph)',
        selected: true,
      })
    ).toBeInTheDocument()
    expect(screen.getByLabelText('Application secret')).toHaveValue('')
    expect(
      screen.queryByLabelText('SMTP password or authorization code')
    ).not.toBeInTheDocument()
    const user = userEvent.setup()
    await user.clear(screen.getByLabelText('Sender mailbox'))
    await user.type(screen.getByLabelText('Sender mailbox'), 'new@example.com')
    await user.click(
      screen.getByRole('button', { name: 'Save email settings' })
    )
    await screen.findByText(
      'Email settings saved. Sending has not been verified for this version.'
    )
    expect(api.put).toHaveBeenCalledTimes(1)
    const body = vi.mocked(api.put).mock.calls[0][1] as EmailUpdate
    expect(body.intent).toMatchObject({
      expected_revision: 4,
      client_secret_action: 'keep',
      legacy_password_action: 'keep',
    })
    expect(body.client_secret).toBe('')
    expect(verify).toHaveBeenCalledWith({
      scope: 'email.delivery.settings.write',
      context: body.intent,
    })
  })
  it('requires a new secret when changing application and clears a submitted replacement after success', async () => {
    await renderSettings()
    const user = userEvent.setup()
    await user.clear(screen.getByLabelText('Application ID'))
    await user.type(
      screen.getByLabelText('Application ID'),
      '33333333-3333-3333-3333-333333333333'
    )
    await user.click(
      screen.getByRole('button', { name: 'Save email settings' })
    )
    await screen.findByText(
      'Enter a new secret when changing the Microsoft application'
    )
    expect(api.put).not.toHaveBeenCalled()
    await user.type(
      screen.getByLabelText('Application secret'),
      'replacement-secret'
    )
    await user.click(
      screen.getByRole('button', { name: 'Save email settings' })
    )
    await screen.findByText(
      'Email settings saved. Sending has not been verified for this version.'
    )
    expect(screen.getByLabelText('Application secret')).toHaveValue('')
    expect(
      (vi.mocked(api.put).mock.calls[0][1] as EmailUpdate).intent
        .client_secret_action
    ).toBe('replace')
    expect(
      JSON.stringify(client.getQueryData(['email-delivery-settings']))
    ).not.toContain('replacement-secret')
  })
  it('cancels sensitive verification without sending a save request', async () => {
    verify.mockResolvedValue(null)
    await renderSettings()
    await userEvent.click(
      screen.getByRole('button', { name: 'Save email settings' })
    )
    await waitFor(() => expect(verify).toHaveBeenCalledTimes(1))
    expect(api.put).not.toHaveBeenCalled()
  })
  it('retains the draft on a version conflict instead of overwriting the newer server configuration', async () => {
    vi.mocked(api.put).mockRejectedValue({
      response: {
        status: 409,
        data: {
          success: false,
          code: 'EMAIL_CONFIG_CONFLICT',
          message: 'Settings changed.',
        },
      },
    })
    await renderSettings()
    const user = userEvent.setup()
    await user.type(
      screen.getByLabelText('Application secret'),
      'keep-my-draft'
    )
    await user.click(
      screen.getByRole('button', { name: 'Save email settings' })
    )
    await screen.findByText(
      'Unable to save email settings. Check the fields; if settings changed elsewhere, reload and compare before saving.'
    )
    expect(screen.getByLabelText('Application secret')).toHaveValue(
      'keep-my-draft'
    )
    expect(api.put).toHaveBeenCalledTimes(1)
  })
  it('disables testing unsaved changes and submits only the saved version and one recipient', async () => {
    await renderSettings()
    const user = userEvent.setup()
    await user.type(
      screen.getByLabelText('Test recipient'),
      'receiver@example.com'
    )
    await user.type(screen.getByLabelText('Sender mailbox'), 'x')
    expect(
      screen.getByRole('button', { name: 'Send test email' })
    ).toBeDisabled()
    await user.click(
      screen.getByRole('button', { name: 'Reload saved email settings' })
    )
    await waitFor(() =>
      expect(
        screen.getByRole('button', { name: 'Send test email' })
      ).toBeEnabled()
    )
    await user.click(screen.getByRole('button', { name: 'Send test email' }))
    await screen.findByText(
      'The mail provider accepted the email. Check the recipient inbox for delivery.'
    )
    expect(
      screen.getByText('Microsoft Graph submission: Passed')
    ).toBeInTheDocument()
    expect(api.post).toHaveBeenCalledWith(
      '/api/option/email_delivery/test',
      { expected_revision: 4, recipient: 'receiver@example.com' },
      { headers: { 'X-Security-Proof': 'scoped-proof' } }
    )
  })
  it('preserves legacy fields and requires confirmation before clearing the Microsoft secret', async () => {
    await renderSettings()
    const user = userEvent.setup()
    expect(
      screen.getByRole('button', { name: 'Clear saved credential' })
    ).toBeDisabled()
    await user.selectOptions(
      screen.getByLabelText('Authentication method'),
      'legacy'
    )
    expect(screen.getByLabelText('SMTP Host')).toHaveValue('smtp.example.com')
    await user.click(
      screen.getByRole('button', {
        name: 'Clear saved Microsoft application secret',
      })
    )
    await user.click(screen.getByRole('button', { name: /^Clear$/ }))
    await user.click(
      screen.getByRole('button', { name: 'Save email settings' })
    )
    await screen.findByText(
      'Email settings saved. Sending has not been verified for this version.'
    )
    expect(
      (vi.mocked(api.put).mock.calls[0][1] as EmailUpdate).intent
    ).toMatchObject({
      client_secret_action: 'clear',
      options: { auth_mode: 'legacy' },
    })
  })
  it('shows Graph permission guidance without claiming delivery', async () => {
    vi.mocked(api.post).mockResolvedValue({
      data: {
        success: true,
        data: {
          revision: 4,
          timestamp: 1793880000,
          request_id: 'graph-access-denied',
          code: 'EMAIL_DELIVERY_FAILED',
          reason: 'GRAPH_ACCESS_DENIED',
          stages: [{ name: 'graph_submit', status: 'failed' }],
        },
      },
    })
    await renderSettings()
    const user = userEvent.setup()
    await user.type(
      screen.getByLabelText('Test recipient'),
      'receiver@example.com'
    )
    await user.click(screen.getByRole('button', { name: 'Send test email' }))
    await screen.findByText(
      'Microsoft Graph denied access. Check Mail.Send permission and access to the sender mailbox.'
    )
    expect(
      screen.queryByText(
        'The mail provider accepted the email. Check the recipient inbox for delivery.'
      )
    ).not.toBeInTheDocument()
  })
  it('marks a previous diagnostic as stale after saving a new configuration', async () => {
    await renderSettings()
    const user = userEvent.setup()
    await user.type(
      screen.getByLabelText('Test recipient'),
      'receiver@example.com'
    )
    await user.click(screen.getByRole('button', { name: 'Send test email' }))
    await screen.findByText(
      'The mail provider accepted the email. Check the recipient inbox for delivery.'
    )
    await user.click(
      screen.getByRole('button', { name: 'Save email settings' })
    )
    await screen.findByText(
      'This result belongs to an older configuration. Test again after saving.'
    )
  })
  it('keeps the visible snapshot and blocks saving or testing after an external refresh until reload', async () => {
    await renderSettings()
    const user = userEvent.setup()
    await user.type(
      screen.getByLabelText('Test recipient'),
      'receiver@example.com'
    )
    settings = {
      ...settings,
      revision: 5,
      options: {
        ...settings.options,
        microsoft: {
          ...settings.options.microsoft,
          mailbox: 'updated@example.com',
        },
      },
    }
    await act(() => {
      client.setQueryData(['email-delivery-settings'], settings)
    })
    await screen.findByText(
      'Email settings changed elsewhere. Reload saved settings before saving or testing.'
    )
    expect(screen.getByLabelText('Sender mailbox')).toHaveValue(
      'sender@example.com'
    )
    expect(
      screen.getByRole('button', { name: 'Send test email' })
    ).toBeDisabled()
    expect(
      screen.getByRole('button', { name: 'Save email settings' })
    ).toBeDisabled()
    await screen.findByText(
      'Email settings changed elsewhere. Reload saved settings before saving or testing.'
    )
    await user.click(
      screen.getByRole('button', { name: 'Reload saved email settings' })
    )
    await waitFor(() =>
      expect(screen.getByLabelText('Sender mailbox')).toHaveValue(
        'updated@example.com'
      )
    )
    await user.click(screen.getByRole('button', { name: 'Send test email' }))
    await waitFor(() =>
      expect(api.post).toHaveBeenCalledWith(
        '/api/option/email_delivery/test',
        { expected_revision: 5, recipient: 'receiver@example.com' },
        { headers: { 'X-Security-Proof': 'scoped-proof' } }
      )
    )
  })
  it('preserves an unsaved secret and mailbox when a newer configuration arrives', async () => {
    await renderSettings()
    const user = userEvent.setup()
    await user.clear(screen.getByLabelText('Sender mailbox'))
    await user.type(
      screen.getByLabelText('Sender mailbox'),
      'draft@example.com'
    )
    await user.type(
      screen.getByLabelText('Application secret'),
      'unsaved-secret'
    )
    settings = { ...settings, revision: 5 }
    await act(() => {
      client.setQueryData(['email-delivery-settings'], settings)
    })
    await screen.findByText(
      'Email settings changed elsewhere. Reload saved settings before saving or testing.'
    )
    expect(screen.getByLabelText('Sender mailbox')).toHaveValue(
      'draft@example.com'
    )
    expect(screen.getByLabelText('Application secret')).toHaveValue(
      'unsaved-secret'
    )
    expect(
      screen.getByRole('button', { name: 'Save email settings' })
    ).toBeDisabled()
    expect(api.put).not.toHaveBeenCalled()
  })
  it('explains how to restore the encryption key when saved credentials cannot be loaded', async () => {
    vi.mocked(api.get).mockRejectedValue({
      response: {
        status: 503,
        data: { success: false, code: 'EMAIL_ENCRYPTION_UNAVAILABLE' },
      },
    })
    const root = createRootRoute({ component: EmailDeliverySettingsSection })
    const router = createRouter({
      routeTree: root,
      history: createMemoryHistory({ initialEntries: ['/'] }),
    })
    render(
      <QueryClientProvider client={client}>
        <RouterProvider router={router} />
      </QueryClientProvider>
    )
    await act(() => router.load())
    await screen.findByText(
      'Email credentials cannot be decrypted. Restore the original EMAIL_DELIVERY_STORAGE_KEY on every server and retry.'
    )
    expect(
      screen.queryByLabelText('Application secret')
    ).not.toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: 'Send test email' })
    ).not.toBeInTheDocument()
  })
  it('updates visible labels when switching between English and Simplified Chinese', async () => {
    await renderSettings()
    await act(() => i18next.changeLanguage('zh'))
    expect(screen.getByLabelText('认证方式')).toBeInTheDocument()
    expect(screen.getByLabelText('应用密钥')).toBeInTheDocument()
  })
})
