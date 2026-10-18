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
import { api } from '@/lib/api'
import { requireServerSuccess } from '@/lib/server-error-message'

export type VideoEnhanceSettings = {
  version: number
  enabled: boolean
  queue_id: string
  region: string
  endpoint: string
  bucket: string
  prefix: string
  enhanced_prefix: string
  store_generated_videos: boolean
  mediakit_configured: boolean
  oss_configured: boolean
  encryption_configured: boolean
}
export type VideoEnhanceUpdate = VideoEnhanceSettings & {
  mediakit_key: string
  access_key_id: string
  access_key_secret: string
}
export const videoEnhanceSettingsKey = ['video-enhance-settings'] as const

export async function getVideoEnhanceSettings() {
  const response = await api.get<{
    success: boolean
    message?: string
    data: VideoEnhanceSettings
  }>('/api/option/video_enhance')
  return requireServerSuccess(response.data).data
}
export async function saveVideoEnhanceSettings(
  values: VideoEnhanceUpdate,
  proof: string
) {
  const response = await api.put<{
    success: boolean
    message?: string
    data: VideoEnhanceSettings
  }>('/api/option/video_enhance', values, {
    headers: { 'X-Security-Proof': proof },
  })
  return requireServerSuccess(response.data).data
}
export async function testVideoEnhanceStorage(proof: string) {
  const response = await api.post<{ success: boolean; message?: string }>(
    '/api/option/video_enhance/test_storage',
    {},
    { headers: { 'X-Security-Proof': proof } }
  )
  return requireServerSuccess(response.data)
}
