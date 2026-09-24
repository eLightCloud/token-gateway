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
import { describe, expect, test } from 'vitest'

import {
  CHANNEL_FORM_DEFAULT_VALUES,
  buildSettingJSON,
  channelFormSchema,
  transformChannelToFormDefaults,
} from '../channel-form'
import type { Channel } from '../../types'

function doubaoChannel(setting: Record<string, unknown>): Channel {
  return {
    ...({} as Channel),
    id: 1,
    type: 54,
    name: 'Doubao',
    key: '',
    status: 1,
    models: 'doubao-seedance-2-0-260128',
    group: 'default',
    setting: JSON.stringify(setting),
    channel_info: { multi_key_mode: 'random', multi_key_polling_index: 0 },
  } as unknown as Channel
}

describe('video upstream channel settings', () => {
  test('defaults keep ark semantics and serialize to no video keys', () => {
    const json = JSON.parse(buildSettingJSON(CHANNEL_FORM_DEFAULT_VALUES))
    expect(json.video_upstream_protocol).toBeUndefined()
    expect(json.video_upstream_profile).toBeUndefined()
  })

  test('edit round-trips an openai_video zapgogo channel', () => {
    const values = transformChannelToFormDefaults(
      doubaoChannel({
        video_upstream_protocol: 'openai_video',
        video_upstream_profile: 'seedance_zapgogo',
      })
    )
    expect(values.video_upstream_protocol).toBe('openai_video')
    expect(values.video_upstream_profile).toBe('seedance_zapgogo')

    const json = JSON.parse(buildSettingJSON(values))
    expect(json.video_upstream_protocol).toBe('openai_video')
    expect(json.video_upstream_profile).toBe('seedance_zapgogo')
  })

  test('a standard openai_video channel omits the profile key', () => {
    const json = JSON.parse(
      buildSettingJSON({
        ...CHANNEL_FORM_DEFAULT_VALUES,
        video_upstream_protocol: 'openai_video',
      })
    )
    expect(json.video_upstream_protocol).toBe('openai_video')
    expect(json.video_upstream_profile).toBeUndefined()
  })

  test('an ark channel never serializes a profile', () => {
    const json = JSON.parse(
      buildSettingJSON({
        ...CHANNEL_FORM_DEFAULT_VALUES,
        video_upstream_protocol: 'ark',
        video_upstream_profile: 'seedance_zapgogo',
      })
    )
    expect(json.video_upstream_protocol).toBeUndefined()
    expect(json.video_upstream_profile).toBeUndefined()
  })

  test('a profile without the openai_video protocol is rejected', () => {
    const result = channelFormSchema.safeParse({
      ...CHANNEL_FORM_DEFAULT_VALUES,
      video_upstream_profile: 'seedance_zapgogo',
    })
    expect(result.success).toBe(false)
  })

  test('unknown stored profile values fall back to standard', () => {
    const values = transformChannelToFormDefaults(
      doubaoChannel({
        video_upstream_protocol: 'openai_video',
        video_upstream_profile: 'vendor_x',
      })
    )
    expect(values.video_upstream_profile).toBe('standard')
  })
})
