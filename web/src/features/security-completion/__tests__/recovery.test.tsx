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
