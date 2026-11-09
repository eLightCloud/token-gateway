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
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18next from 'i18next'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import zh from '@/i18n/locales/zh.json'

import {
  type OperationsResponse,
  getOperations,
  retryOperationsDelivery,
  saveOperations,
} from '../api'
import { OperationsSettingsSection } from '../settings-section'

vi.mock('../api', () => ({
  operationsQueryKey: ['operations-notifications'],
  getOperations: vi.fn(),
  saveOperations: vi.fn(),
  retryOperationsDelivery: vi.fn(),
}))
let data: OperationsResponse
beforeEach(async () => {
  await i18next.changeLanguage('en')
  vi.clearAllMocks()
  data = {
    settings: {
      daily_enabled: false,
      emergency_enabled: false,
      preview_only: true,
      recipients: '',
    },
    runtime: { mail_ready: true, paused: false },
    deliveries: [],
  }
  vi.mocked(getOperations).mockImplementation(async () => data)
  vi.mocked(saveOperations).mockImplementation(async (settings) => settings)
  vi.mocked(retryOperationsDelivery).mockResolvedValue(null)
})
function renderSettings() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <OperationsSettingsSection />
    </QueryClientProvider>
  )
}
describe('Operations notification settings', () => {
  it('keeps daily and emergency email independently disabled until saved', async () => {
    const user = userEvent.setup()
    renderSettings()
    const daily = await screen.findByRole('switch', {
      name: 'Enable daily summaries',
    })
    expect(daily.getAttribute('aria-checked')).toBe('false')
    const save = screen.getByRole('button', { name: 'Save' })
    expect(save.hasAttribute('disabled')).toBe(true)
    await user.click(daily)
    await user.type(
      screen.getByLabelText('Administrator email addresses'),
      'admin@example.com'
    )
    await user.click(save)
    await waitFor(() =>
      expect(saveOperations).toHaveBeenCalledWith(
        {
          daily_enabled: true,
          emergency_enabled: false,
          preview_only: true,
          recipients: 'admin@example.com',
        },
        expect.anything()
      )
    )
  })
  it('shows paused processing and requires confirmation before retrying unknown delivery', async () => {
    const user = userEvent.setup()
    data.runtime.paused = true
    data.deliveries = [
      {
        key: '2026-11-05:user:1',
        day: '2026-11-05',
        kind: 'account',
        status: 'unknown',
        recipient: 'user@example.com',
        subject: 'Summary',
        body: '<p>Safe content</p><script>bad()</script>',
        reason: 'EMAIL_DELIVERY_UNKNOWN',
      },
    ]
    renderSettings()
    await screen.findByText(
      'Daily processing is paused while resource usage is high or samples are unavailable.'
    )
    await user.click(screen.getByRole('button', { name: 'Retry' }))
    expect(retryOperationsDelivery).not.toHaveBeenCalled()
    await screen.findByText(
      'An unknown delivery may already have reached the recipient. Retry only after checking. Preview mode never sends daily email.'
    )
    await user.click(screen.getByRole('button', { name: 'Continue' }))
    await waitFor(() => expect(retryOperationsDelivery).toHaveBeenCalled())
  })
  it('shows an error with retry when loading fails', async () => {
    vi.mocked(getOperations).mockRejectedValue(new Error('unavailable'))
    renderSettings()
    await screen.findByText('Failed to load settings')
    expect(screen.getByRole('button', { name: 'Retry' })).toBeTruthy()
  })
})

afterEach(async () => {
  await i18next.changeLanguage('en')
})
it('uses Chinese labels when the interface language changes', async () => {
  i18next.addResourceBundle('zh', 'translation', zh.translation, true, true)
  await i18next.changeLanguage('zh')
  renderSettings()
  await screen.findByRole('columnheader', { name: '收件人' })
  expect(
    screen.getByRole('switch', { name: '启用管理员应急邮件' })
  ).toBeTruthy()
})
