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
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import i18next from 'i18next'
import { beforeAll, beforeEach, expect, test, vi } from 'vitest'

import { SettingsPageProvider } from '../../components/settings-page-context'
import { getVideoEnhanceSettings, saveVideoEnhanceSettings } from '../api'
import { VideoEnhanceSettingsSection } from '../settings-section'

vi.mock('../api', () => ({
  videoEnhanceSettingsKey: ['video-enhance-settings'],
  getVideoEnhanceSettings: vi.fn(),
  saveVideoEnhanceSettings: vi.fn(),
  testVideoEnhanceStorage: vi.fn(),
}))
vi.mock('@/features/auth/secure-verification', () => ({
  SecureVerificationDialog: () => null,
  useSecureVerification: () => ({
    requestVerification: vi
      .fn()
      .mockResolvedValue({ proof_token: 'one-use-proof' }),
    dialogProps: {},
  }),
}))

beforeAll(async () => {
  await i18next.init({
    lng: 'en',
    resources: { en: { translation: {} } },
    keySeparator: false,
  })
})
beforeEach(() => {
  vi.mocked(getVideoEnhanceSettings).mockResolvedValue({
    version: 1,
    enabled: false,
    queue_id: '',
    region: 'cn-hangzhou',
    endpoint: '',
    bucket: 'test-video',
    prefix: 'sd',
    enhanced_prefix: 'sd-enhance',
    store_generated_videos: true,
    mediakit_configured: true,
    oss_configured: true,
    encryption_configured: true,
  })
})

function renderSettings() {
  const actions = document.createElement('div')
  document.body.append(actions)
  return render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <SettingsPageProvider actionsContainer={actions}>
        <VideoEnhanceSettingsSection />
      </SettingsPageProvider>
    </QueryClientProvider>
  )
}

test('configured credentials stay blank and dirty storage settings cannot be tested before saving', async () => {
  renderSettings()
  expect(await screen.findByLabelText('MediaKit API key')).toHaveValue('')
  expect(screen.getByLabelText('OSS access key secret')).toHaveValue('')
  expect(
    screen.getByRole('switch', {
      name: 'Store all Seedance generated videos in OSS',
    })
  ).toBeChecked()
  fireEvent.change(screen.getByLabelText('OSS bucket'), {
    target: { value: 'changed-bucket' },
  })
  expect(
    screen.getByRole('button', { name: 'Test saved OSS configuration' })
  ).toBeDisabled()
})

test('saving uses the existing security proof and clears entered credentials on success', async () => {
  vi.mocked(saveVideoEnhanceSettings).mockResolvedValue({
    version: 2,
    enabled: false,
    queue_id: '',
    region: 'cn-hangzhou',
    endpoint: '',
    bucket: 'test-video',
    prefix: 'sd',
    enhanced_prefix: 'sd-enhance',
    store_generated_videos: true,
    mediakit_configured: true,
    oss_configured: true,
    encryption_configured: true,
  })
  renderSettings()
  expect(await screen.findByLabelText('OSS source video prefix')).toHaveValue(
    'sd'
  )
  expect(screen.getByLabelText('OSS enhanced video prefix')).toHaveValue(
    'sd-enhance'
  )
  fireEvent.change(screen.getByLabelText('OSS enhanced video prefix'), {
    target: { value: 'enhanced-output' },
  })
  fireEvent.change(await screen.findByLabelText('MediaKit API key'), {
    target: { value: 'temporary-key' },
  })
  fireEvent.click(screen.getByRole('button', { name: 'Save Changes' }))
  await waitFor(() =>
    expect(saveVideoEnhanceSettings).toHaveBeenCalledWith(
      expect.objectContaining({
        mediakit_key: 'temporary-key',
        prefix: 'sd',
        enhanced_prefix: 'enhanced-output',
        store_generated_videos: true,
      }),
      'one-use-proof'
    )
  )
  await waitFor(() =>
    expect(screen.getByLabelText('MediaKit API key')).toHaveValue('')
  )
})
