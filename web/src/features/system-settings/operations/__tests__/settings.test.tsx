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
  getOperationsDeliveries,
  retryOperationsDelivery,
  saveOperations,
} from '../api'
import { OperationsSettingsSection } from '../settings-section'

vi.mock('../api', () => ({
  operationsQueryKey: ['operations-notifications'],
  operationsDeliveriesQueryKey: ['operations-notification-deliveries'],
  getOperations: vi.fn(),
  getOperationsDeliveries: vi.fn(),
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
  }
  vi.mocked(getOperations).mockImplementation(async () => data)
  vi.mocked(getOperationsDeliveries).mockResolvedValue({ items: [], total: 0 })
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
    vi.mocked(getOperationsDeliveries).mockResolvedValue({
      total: 1,
      items: [
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
      ],
    })
    renderSettings()
    await screen.findByText(
      'Daily processing is paused while resource usage is high or samples are unavailable.'
    )
    await user.click(await screen.findByRole('button', { name: 'Retry' }))
    expect(retryOperationsDelivery).not.toHaveBeenCalled()
    await screen.findByText(
      'An unknown delivery may already have reached the recipient. Retry only after checking. Preview mode never sends daily email.'
    )
    await user.click(screen.getByRole('button', { name: 'Continue' }))
    await waitFor(() => expect(retryOperationsDelivery).toHaveBeenCalled())
  })
  it('fetches the requested page and refreshes it without discarding settings edits', async () => {
    vi.mocked(getOperationsDeliveries).mockImplementation(async (page) => ({
      total: 11,
      items: [
        {
          key: String(page),
          day: '2026-11-27',
          kind: 'admin',
          status: 'accepted',
          recipient: `page-${page}@example.com`,
          subject: 'Notification',
          body: '<p>Notification body</p>',
          reason: '',
        },
      ],
    }))
    const user = userEvent.setup()
    renderSettings()
    expect(await screen.findByText('page-1@example.com')).toBeVisible()
    expect(getOperationsDeliveries).toHaveBeenCalledTimes(1)
    expect(getOperationsDeliveries).toHaveBeenLastCalledWith(
      1,
      10,
      expect.any(AbortSignal)
    )

    const recipients = screen.getByLabelText('Administrator email addresses')
    await user.type(recipients, 'draft@example.com')
    await user.click(screen.getByRole('button', { name: 'Go to next page' }))
    expect(await screen.findByText('page-2@example.com')).toBeVisible()
    expect(screen.queryByText('page-1@example.com')).not.toBeInTheDocument()
    expect(getOperationsDeliveries).toHaveBeenLastCalledWith(
      2,
      10,
      expect.any(AbortSignal)
    )
    expect(
      screen.getByRole('button', { name: 'Go to next page' })
    ).toBeDisabled()

    await user.click(screen.getByRole('button', { name: 'Refresh' }))
    await waitFor(() =>
      expect(getOperationsDeliveries).toHaveBeenCalledTimes(3)
    )
    expect(getOperationsDeliveries).toHaveBeenLastCalledWith(
      2,
      10,
      expect.any(AbortSignal)
    )
    expect(recipients).toHaveValue('draft@example.com')
    await user.click(screen.getByRole('button', { name: 'Preview' }))
    expect(await screen.findByText('Notification body')).toBeVisible()
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
