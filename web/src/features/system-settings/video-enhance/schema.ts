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
import { z } from 'zod'

export const videoEnhanceSettingsSchema = z.object({
  version: z.number().int().nonnegative(),
  enabled: z.boolean(),
  queue_id: z.string().max(128),
  region: z.string().max(64),
  endpoint: z.string().max(256),
  bucket: z.string().max(63),
  prefix: z.string().max(128),
  enhanced_prefix: z.string().max(128),
  store_generated_videos: z.boolean(),
  mediakit_key: z.string().max(4096),
  access_key_id: z.string().max(4096),
  access_key_secret: z.string().max(4096),
  mediakit_configured: z.boolean(),
  oss_configured: z.boolean(),
  encryption_configured: z.boolean(),
})
