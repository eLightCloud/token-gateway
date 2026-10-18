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
import { createRequire } from 'node:module'
import { resolve } from 'node:path'

import { zodResolver } from '@hookform/resolvers/zod'
import { fireEvent, render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18next from 'i18next'
import { useForm } from 'react-hook-form'
import { beforeAll, expect, test, vi } from 'vitest'

import { Form } from '@/components/ui/form'
import zh from '@/i18n/locales/zh.json'

import {
  getChannelConfigurationSection,
  getChannelConfigurationState,
} from '../../lib/channel-configuration'
import {
  CHANNEL_FORM_DEFAULT_VALUES,
  buildSettingJSON,
  channelFormSchema,
  transformChannelToFormDefaults,
  type ChannelFormValues,
} from '../../lib/channel-form'
import type { Channel } from '../../types'
import { VideoEnhanceFields } from '../drawers/video-enhance-fields'
import { VideoUpstreamFields } from '../drawers/video-upstream-fields'
import { VideoEnhanceModels } from '../video-enhance-models'

beforeAll(async () => {
  await i18next.init({
    lng: 'en',
    resources: { en: { translation: {} }, zh },
    keySeparator: false,
  })
})

function EnhancementForm(props: {
  locked?: boolean
  type?: number
  enabled?: boolean
  plugin?: string
  models?: string
  supplier?: 'volcengine' | 'runyyitd' | ''
  onSave?: (values: ChannelFormValues) => void
}) {
  const form = useForm<ChannelFormValues>({
    resolver: zodResolver(channelFormSchema),
    defaultValues: {
      ...CHANNEL_FORM_DEFAULT_VALUES,
      name: 'Seedance channel',
      type: props.type ?? 54,
      task_plugin_key: props.plugin,
      models: props.models ?? 'doubao-seedance-2-0-mini-260615',
      video_enhance: {
        enabled: props.enabled ?? false,
        supplier: props.supplier ?? 'volcengine',
      },
    },
  })
  return (
    <Form {...form}>
      <form onSubmit={form.handleSubmit((values) => props.onSave?.(values))}>
        <VideoUpstreamFields
          form={form}
          locked={Boolean(props.locked)}
          channelType={props.type ?? 54}
          pluginKey={props.plugin}
        />
        <VideoEnhanceFields
          form={form}
          locked={Boolean(props.locked)}
          channelType={props.type ?? 54}
          pluginKey={props.plugin}
        />
        <button type='submit'>Save channel</button>
      </form>
    </Form>
  )
}

test('enabling enhancement keeps the base model and shows its derived alias', () => {
  render(<EnhancementForm />)
  expect(
    screen.getByLabelText('Channel video generation supplier')
  ).toBeVisible()
  fireEvent.click(
    screen.getByRole('switch', { name: 'Seedance video enhancement' })
  )
  expect(
    screen.getByText(
      'doubao-seedance-2-0-mini-260615 → doubao-seedance-2-0-mini-260615-enhance'
    )
  ).toBeInTheDocument()
  expect(
    within(
      screen.getByRole('group', { name: 'Seedance video enhancement' })
    ).queryByRole('combobox')
  ).toBeNull()
  expect(
    screen.getByText(
      'Generation uses this channel (Volcengine official); enhancement uses MediaKit.'
    )
  ).toBeVisible()
})

test('channel previews include every enhancement model published by the plugin', () => {
  const plugin: {
    extendSeedanceEnhanceMetadata: (meta: {
      models: string[]
      usageProfiles: unknown[]
      protocols: { name: string; models: string[] }[]
    }) => void
  } = createRequire(resolve('package.json'))(
    '../plugins/local/seedance_enhance.js'
  )
  const meta = {
    models: [] as string[],
    usageProfiles: [] as unknown[],
    protocols: [{ name: 'openai_video', models: [] as string[] }],
  }
  plugin.extendSeedanceEnhanceMetadata(meta)
  expect(meta.models.length).toBeGreaterThan(0)
  const baseModels = meta.models.map((name) => name.replace(/-enhance$/, ''))
  render(
    <EnhancementForm
      enabled
      models={[
        ...baseModels,
        'doubao-seedance-1-5-pro-250528',
        'sd-2-mini',
        'doubao-seedance-2-6-future',
      ].join(',')}
    />
  )
  expect(screen.getAllByRole('listitem')).toHaveLength(meta.models.length)
  for (const base of baseModels) {
    expect(screen.getByText(`${base} → ${base}-enhance`)).toBeInTheDocument()
  }
})

test('a locked channel cannot toggle enhancement', () => {
  render(<EnhancementForm locked enabled />)
  const toggle = screen.getByRole('switch', {
    name: 'Seedance video enhancement',
  })
  expect(toggle).toHaveAttribute('aria-disabled', 'true')
  fireEvent.click(toggle)
  expect(toggle).toHaveAttribute('aria-checked', 'true')
  expect(
    screen.getByRole('combobox', { name: 'Channel video generation supplier' })
  ).toBeDisabled()
})

test('a task-plugin channel bound to another plugin does not offer Seedance enhancement', () => {
  render(<EnhancementForm type={61} plugin='sora' />)
  expect(screen.queryByRole('switch')).toBeNull()
})

test('switching to an unrelated channel omits hidden enhancement settings', () => {
  const values: ChannelFormValues = {
    ...CHANNEL_FORM_DEFAULT_VALUES,
    type: 54,
    video_enhance: { enabled: true, supplier: 'volcengine' },
  }
  expect(JSON.parse(buildSettingJSON(values)).video_enhance).toEqual(
    values.video_enhance
  )
  expect(
    JSON.parse(buildSettingJSON({ ...values, type: 1 })).video_enhance
  ).toBeUndefined()
})

test('enabling enhancement without a supplier fails on the supplier field in other settings', () => {
  const values: ChannelFormValues = {
    ...CHANNEL_FORM_DEFAULT_VALUES,
    name: 'Seedance channel',
    type: 54,
    models: 'doubao-seedance-2-0-260128',
    video_enhance: { enabled: true, supplier: '' },
  }
  const result = channelFormSchema.safeParse(values)
  expect(result.success).toBe(false)
  if (!result.success) {
    expect(result.error.issues).toContainEqual(
      expect.objectContaining({
        path: ['video_enhance', 'supplier'],
        message:
          "Select this channel's video generation supplier before enabling enhancement.",
      })
    )
  }
  expect(getChannelConfigurationSection('video_enhance')).toBe('other')
  expect(getChannelConfigurationSection('video_enhance.supplier')).toBe('other')
  const state = getChannelConfigurationState(
    values,
    {
      video_enhance: { supplier: { type: 'custom', message: 'Required' } },
    },
    true
  )
  expect(state.blocks.extraSettings).toBe('error')
  expect(state.sections.other).toBe('error')
})

test('a stored Runy supplier survives disabling, reopening and re-enabling enhancement', () => {
  const channel = {
    id: 140,
    type: 54,
    status: 1,
    name: 'Seedance',
    models: 'doubao-seedance-2-0-260128',
    group: 'default',
    channel_info: { multi_key_mode: 'random', multi_key_polling_index: 0 },
    setting: JSON.stringify({
      video_enhance: { enabled: false, supplier: 'runyyitd' },
    }),
  } as Channel
  const values = transformChannelToFormDefaults(channel)
  expect(values.video_enhance).toEqual({ enabled: false, supplier: 'runyyitd' })
  const reopened = transformChannelToFormDefaults({
    ...channel,
    setting: buildSettingJSON(values),
  })
  if (!reopened.video_enhance) {
    throw new Error('Missing persisted video settings')
  }
  reopened.video_enhance.enabled = true
  expect(channelFormSchema.safeParse(reopened).success).toBe(true)
  expect(JSON.parse(buildSettingJSON(reopened)).video_enhance).toEqual({
    enabled: true,
    supplier: 'runyyitd',
  })
})

test('a legacy channel can select its supplier once and save enhancement without a second selector', async () => {
  const user = userEvent.setup()
  const save = vi.fn()
  render(<EnhancementForm enabled supplier='' onSave={save} />)
  await user.click(screen.getByRole('button', { name: 'Save channel' }))
  const supplier = screen.getByRole('combobox', {
    name: 'Channel video generation supplier',
  })
  expect(
    await screen.findAllByText(
      "Select this channel's video generation supplier before enabling enhancement."
    )
  ).toHaveLength(2)
  expect(supplier).toHaveAttribute('aria-invalid', 'true')
  expect(save).not.toHaveBeenCalled()
  await user.click(supplier)
  await user.click(screen.getByRole('option', { name: 'runyyitd' }))
  await user.click(screen.getByRole('button', { name: 'Save channel' }))
  expect(save).toHaveBeenCalledWith(
    expect.objectContaining({
      video_enhance: { enabled: true, supplier: 'runyyitd' },
    })
  )
  expect(
    screen.queryByText(
      "Select this channel's video generation supplier before enabling enhancement."
    )
  ).toBeNull()
  expect(
    within(
      screen.getByRole('group', { name: 'Seedance video enhancement' })
    ).queryByRole('combobox')
  ).toBeNull()
  expect(
    screen.getByText(
      'Generation uses this channel (runyyitd); enhancement uses MediaKit.'
    )
  ).toBeVisible()
})

test('enabling and disabling enhancement reuses the channel supplier', async () => {
  const user = userEvent.setup()
  const save = vi.fn()
  render(<EnhancementForm supplier='runyyitd' onSave={save} />)
  const toggle = screen.getByRole('switch', {
    name: 'Seedance video enhancement',
  })
  await user.click(toggle)
  await user.click(screen.getByRole('button', { name: 'Save channel' }))
  expect(save).toHaveBeenLastCalledWith(
    expect.objectContaining({
      video_enhance: { enabled: true, supplier: 'runyyitd' },
    })
  )
  await user.click(toggle)
  await user.click(screen.getByRole('button', { name: 'Save channel' }))
  expect(save).toHaveBeenLastCalledWith(
    expect.objectContaining({
      video_enhance: { enabled: false, supplier: 'runyyitd' },
    })
  )
})

test('the missing supplier validation is localized when switching to Chinese', async () => {
  const user = userEvent.setup()
  render(<EnhancementForm enabled supplier='' />)
  try {
    await i18next.changeLanguage('zh')
    await user.click(screen.getByRole('button', { name: 'Save channel' }))
    const supplier = screen.getByRole('combobox', {
      name: '渠道视频生成供应商',
    })
    expect(supplier).toHaveAttribute('aria-invalid', 'true')
    expect(
      screen.getAllByText('启用增强前，请先设置此渠道的视频生成供应商')
    ).toHaveLength(2)
  } finally {
    await i18next.changeLanguage('en')
  }
})

test('Volcengine enhancement rejects the Videos protocol before submission', () => {
  const result = channelFormSchema.safeParse({
    ...CHANNEL_FORM_DEFAULT_VALUES,
    name: 'Volcengine',
    type: 54,
    models: 'doubao-seedance-2-0-260128',
    video_enhance: { enabled: true, supplier: 'volcengine' },
    video_upstream_protocol: 'openai_video',
  })
  expect(result.success).toBe(false)
  if (!result.success) {
    expect(result.error.issues).toContainEqual(
      expect.objectContaining({ path: ['video_upstream_protocol'] })
    )
  }
})

test('ordinary and unrelated channels do not require enhancement supplier metadata', () => {
  const values = {
    ...CHANNEL_FORM_DEFAULT_VALUES,
    name: 'Ordinary channel',
    models: 'model',
    video_enhance: { enabled: false, supplier: '' as const },
  }
  expect(channelFormSchema.safeParse({ ...values, type: 54 }).success).toBe(
    true
  )
  expect(
    channelFormSchema.safeParse({
      ...values,
      type: 1,
      video_enhance: { enabled: true, supplier: '' },
    }).success
  ).toBe(true)
})

test('derived model display follows the toggle and base model selection without adding unsupported aliases', () => {
  const base = 'doubao-seedance-2-0-260128'
  const view = render(<VideoEnhanceModels models={[base]} enabled={false} />)
  expect(
    screen.queryByRole('region', { name: 'Enhancement models' })
  ).toBeNull()
  view.rerender(
    <VideoEnhanceModels
      models={[
        base,
        ` ${base} `,
        'custom-model',
        'doubao-seedance-1-5-pro-250528',
        `${base}-enhance`,
      ]}
      enabled
    />
  )
  const region = within(
    screen.getByRole('region', { name: 'Enhancement models' })
  )
  expect(region.getAllByRole('listitem')).toHaveLength(1)
  expect(region.getByText(`${base}-enhance`, { exact: true })).toBeVisible()
  expect(region.getByRole('button', { name: 'Copy model name' })).toBeEnabled()
  view.rerender(<VideoEnhanceModels models={[]} enabled />)
  expect(screen.getByRole('status')).toHaveTextContent(
    'Add a supported Seedance 2.0 or 2.5 base model to publish enhancement models.'
  )
})
