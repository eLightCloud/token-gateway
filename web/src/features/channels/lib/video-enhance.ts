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
import type { RefinementCtx } from 'zod'

import type { ChannelFormValues } from './channel-form'

export const SEEDANCE_ENHANCE_MODELS = [
  'doubao-seedance-2-0-260128',
  'doubao-seedance-2-0-fast-260128',
  'doubao-seedance-2-0-mini-260615',
  'doubao-seedance-2-5-260628',
] as const

export type VideoEnhanceConfig = {
  enabled: boolean
  supplier: '' | 'volcengine' | 'runyyitd'
}

export function getVideoEnhanceBaseModels(models: readonly string[]): string[] {
  return [...new Set(models.map((model) => model.trim()))].filter((model) =>
    SEEDANCE_ENHANCE_MODELS.some((base) => base === model)
  )
}

export function readVideoEnhanceConfig(value: unknown): VideoEnhanceConfig {
  const config = value && typeof value === 'object' ? value : {}
  const enabled = 'enabled' in config && config.enabled === true
  const supplier = 'supplier' in config ? config.supplier : ''
  return {
    enabled,
    supplier:
      supplier === 'volcengine' || supplier === 'runyyitd' ? supplier : '',
  }
}

export function videoEnhanceChannelSupported(
  type: number,
  pluginKey?: string,
  extensionKeys?: string[]
) {
  return (
    type === 54 ||
    type === 45 ||
    ((type === 61 || type === 60) &&
      (pluginKey === 'doubao' || extensionKeys?.includes('doubao') === true))
  )
}

export function validateVideoEnhanceChannel(
  data: Pick<
    ChannelFormValues,
    | 'type'
    | 'task_plugin_key'
    | 'task_extend_plugin_keys'
    | 'video_enhance'
    | 'video_upstream_protocol'
  >,
  ctx: RefinementCtx
) {
  if (
    !videoEnhanceChannelSupported(
      data.type,
      data.task_plugin_key,
      data.task_extend_plugin_keys
    ) ||
    !data.video_enhance?.enabled
  ) {
    return
  }
  if (!data.video_enhance.supplier) {
    ctx.addIssue({
      code: 'custom',
      path: ['video_enhance', 'supplier'],
      message:
        "Select this channel's video generation supplier before enabling enhancement.",
    })
  }
  if (
    data.video_enhance.supplier === 'volcengine' &&
    data.video_upstream_protocol === 'openai_video'
  ) {
    ctx.addIssue({
      code: 'custom',
      path: ['video_upstream_protocol'],
      message: 'Volcengine enhancement generation requires the Ark protocol.',
    })
  }
}
