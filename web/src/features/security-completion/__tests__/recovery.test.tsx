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
import { cleanup, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import { SecurityRecoveryDialog } from '../recovery-dialog'

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
  useAuthStore.getState().auth.reset()
})

it('requires incident, two identity checks and explicit confirmation before requesting recovery', async () => {
  const post = vi.spyOn(api, 'post')
  useAuthStore.getState().auth.setUser({ id: 1, username: 'operator', role: 100 })
  render(<SecurityRecoveryDialog open userID={42} onOpenChange={() => undefined} onSuccess={() => undefined} />)
  expect(screen.getByRole('checkbox', { name: 'Confirmed key compromise: revoke consumption keys' })).not.toBeChecked()
  await userEvent.click(screen.getByRole('button', { name: 'Reset 2FA' }))
  expect(await screen.findByText('Enter an incident reference.')).toBeVisible()
  expect(screen.getByText('Record two independent identity checks.')).toBeVisible()
  expect(screen.getByText('Confirm the identity checks before recovery.')).toBeVisible()
  expect(screen.getByRole('textbox', { name: 'Incident reference' })).toHaveFocus()
  expect(post).not.toHaveBeenCalled()
})

it('keeps email change separate from key compromise and explains pending verification', () => {
  const post = vi.spyOn(api, 'post')
  useAuthStore.getState().auth.setUser({ id: 1, username: 'operator', role: 100 })
  render(<SecurityRecoveryDialog open emailChange userID={42} onOpenChange={() => undefined} onSuccess={() => undefined} />)
  expect(screen.getByRole('textbox', { name: 'New Email' })).toBeVisible()
  expect(screen.queryByRole('checkbox', { name: 'Confirmed key compromise: revoke consumption keys' })).not.toBeInTheDocument()
  expect(screen.getByText('The current email remains unchanged until the new address is verified.')).toBeVisible()
  expect(post).not.toHaveBeenCalled()
})

it('requires documented checks for Passkey recovery instead of direct deletion', async () => {
  const remove = vi.spyOn(api, 'delete')
  useAuthStore
    .getState()
    .auth.setUser({ id: 1, username: 'operator', role: 100 })
  render(
    <SecurityRecoveryDialog
      open
      passkeyRecovery
      userID={42}
      onOpenChange={() => undefined}
      onSuccess={() => undefined}
    />
  )
  await userEvent.click(screen.getByRole('button', { name: 'Reset Passkey' }))
  expect(await screen.findByText('Enter an incident reference.')).toBeVisible()
  expect(
    screen.getByText('Record two independent identity checks.')
  ).toBeVisible()
  expect(remove).not.toHaveBeenCalled()
})

it('binds Passkey recovery verification and deletion to the same incident and key decision', async () => {
  const user = userEvent.setup()
  const onSuccess = vi.fn()
  const context = { user_id: 42, incident: 'SECOND-42', revoke_keys: false }
  const scope = 'account.security.passkey_recover'
  useAuthStore
    .getState()
    .auth.setUser({ id: 1, username: 'operator', role: 100 })
  const methods = vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: {
        scope,
        methods: [{ method: '2fa', available: true }],
        oauth_providers: [],
        password_encryption_enabled: false,
      },
    },
  })
  const verify = vi.spyOn(api, 'post').mockResolvedValue({
    data: {
      success: true,
      data: {
        scope,
        proof_token: 'synthetic-passkey-recovery-proof',
        method: '2fa',
        expires_at: Math.floor(Date.now() / 1000) + 60,
      },
    },
  })
  const remove = vi
    .spyOn(api, 'delete')
    .mockResolvedValue({ data: { success: true, data: { id: 7 } } })
  render(
    <SecurityRecoveryDialog
      open
      passkeyRecovery
      userID={42}
      onOpenChange={() => undefined}
      onSuccess={onSuccess}
    />
  )
  await user.type(
    screen.getByRole('textbox', { name: 'Incident reference' }),
    context.incident
  )
  await user.type(
    screen.getByRole('textbox', { name: 'Identity verification references' }),
    'Two independent synthetic verification references'
  )
  await user.click(
    screen.getByRole('checkbox', {
      name: 'I completed two independent identity checks.',
    })
  )
  await user.click(screen.getByRole('button', { name: 'Reset Passkey' }))
  await user.type(
    await screen.findByLabelText('Authenticator code or backup code'),
    '123456'
  )
  expect(remove).not.toHaveBeenCalled()
  await user.click(screen.getByRole('button', { name: 'Verify' }))
  expect(methods).toHaveBeenCalledWith(
    '/api/verify/methods',
    expect.objectContaining({ params: { scope } })
  )
  expect(verify).toHaveBeenCalledWith(
    '/api/verify',
    expect.objectContaining({ scope, context }),
    expect.anything()
  )
  expect(remove).toHaveBeenCalledWith(
    '/api/user/42/reset_passkey',
    expect.objectContaining({
      headers: { 'X-Security-Proof': 'synthetic-passkey-recovery-proof' },
      data: expect.objectContaining({
        incident: context.incident,
        revoke_keys: context.revoke_keys,
        confirmed: true,
      }),
      singleUseAuthorization: true,
    })
  )
  expect(onSuccess).toHaveBeenCalledOnce()
})
