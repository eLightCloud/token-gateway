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
import assert from 'node:assert/strict'

import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { createInstance } from 'i18next'
import { I18nextProvider } from 'react-i18next'
import { describe, test, expect, vi } from 'vitest'

import en from '@/i18n/locales/en.json'
import zh from '@/i18n/locales/zh.json'

import { UsageSnapshotStatePanels } from '../index'
import type { OrganizationUsageSnapshot } from '../usage-snapshot-api'

async function renderPanel(props: {
  snapshot?: OrganizationUsageSnapshot
  isLoading?: boolean
  error?: Error | null
  onRefresh?: () => void
  language?: 'en' | 'zhCN'
}) {
  const i18n = createInstance()
  await i18n.init({
    lng: props.language ?? 'en',
    resources: { en, zhCN: zh },
    fallbackLng: 'en',
  })
  const view = render(
    <I18nextProvider i18n={i18n}>
      <UsageSnapshotStatePanels
        snapshot={props.snapshot}
        error={props.error}
        isLoading={props.isLoading ?? false}
        onRefresh={props.onRefresh ?? (() => {})}
      />
    </I18nextProvider>
  )
  return { ...view, i18n }
}

describe('usage snapshot state panels', () => {
  test('request failure displays an error and retry instead of empty data', async () => {
    const onRefresh = vi.fn()
    await renderPanel({ error: new Error('connection failed'), onRefresh })
    expect(screen.getByText('connection failed')).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    expect(onRefresh).toHaveBeenCalledTimes(1)
    expect(screen.queryByText('No data')).toBeNull()
  })

  test.each(['queued', 'building'] as const)(
    '%s state with a published payload keeps its statistics visible without a page skeleton',
    async (status) => {
      const view = await renderPanel({
        snapshot: {
          status,
          snapshot_id: 3,
          revision: 2,
          payload_generation: 1,
          range_start: 0,
          range_end: 60,
          requested_end: 0,
          slice_done: 0,
          slice_total: 0,
          payload: {
            total_quota: 350,
            request_count: 3,
            prompt_tokens: 70,
            completion_tokens: 43,
            member_count: 2,
            active_member_count: 2,
            usage_user_count: 2,
            trend: [],
            members: [],
            models: [],
            channels: [],
            models_truncated: false,
            channels_truncated: false,
            covered_until: 60,
            range_start: 0,
            range_end: 60,
            generated_at: 61,
            calculation_version: 2,
          },
        },
      })
      expect(screen.getByText('Previous results')).toBeTruthy()
      expect(screen.getAllByText('Consumption amount').length).toBeGreaterThan(
        0
      )
      expect(screen.getByText('Channel usage')).toBeTruthy()
      expect(view.container.querySelector('[data-slot="skeleton"]')).toBeNull()
      if (status === 'building') {
        expect(screen.getByText('Preparing usage statistics...')).toBeVisible()
        expect(
          screen.getByText(
            'This query will update automatically when processing finishes.'
          )
        ).toBeVisible()
      }
    }
  )

  test('building state reports progress slices instead of zero numbers', async () => {
    await renderPanel({
      snapshot: {
        status: 'building',
        snapshot_id: 1,
        revision: 2,
        payload_generation: 1,
        range_start: 0,
        range_end: 60,
        requested_end: 0,
        slice_done: 3,
        slice_total: 10,
      },
    })
    expect(screen.getByText('Preparing usage statistics...')).toBeVisible()
    expect(screen.getByText(/Prepared slices: 3\/10/)).toBeTruthy()
    assert.equal(screen.queryByText('Consumption amount'), null)
  })

  test('building notice uses production Chinese resources, preserves the published cutoff and disappears when ready', async () => {
    const snapshot: OrganizationUsageSnapshot = {
      status: 'building',
      snapshot_id: 3,
      revision: 2,
      payload_generation: 1,
      range_start: 0,
      range_end: 120,
      requested_end: 120,
      slice_done: 1,
      slice_total: 2,
      payload: {
        total_quota: 350,
        request_count: 3,
        prompt_tokens: 70,
        completion_tokens: 43,
        member_count: 1,
        active_member_count: 1,
        usage_user_count: 1,
        trend: [],
        members: [
          { user_id: 8, username: 'alice', total_quota: 350, request_count: 3 },
        ],
        models: [],
        channels: [],
        models_truncated: false,
        channels_truncated: false,
        covered_until: 60,
        range_start: 0,
        range_end: 60,
        generated_at: 61,
        calculation_version: 2,
      },
    }
    const view = await renderPanel({
      snapshot,
      language: 'zhCN',
      isLoading: true,
    })
    expect(screen.getByText('正在准备用量统计...')).toBeVisible()
    expect(screen.getByText('处理完成后，本次查询将自动更新。')).toBeVisible()
    expect(screen.getByText('上一版结果')).toBeVisible()
    expect(
      screen.getByText('当前展示该范围最近一次已发布的统计结果。')
    ).toBeVisible()
    expect(screen.getByRole('status')).toHaveTextContent(
      '覆盖截至: 1970-01-01 08:01:00'
    )
    expect(screen.getByText('alice')).toBeVisible()
    expect(view.container.querySelector('[data-slot="skeleton"]')).toBeNull()
    await act(async () => {
      await view.i18n.changeLanguage('en')
    })
    expect(screen.getByText('Preparing usage statistics...')).toBeVisible()
    expect(screen.getByText('Previous results')).toBeVisible()
    view.rerender(
      <I18nextProvider i18n={view.i18n}>
        <UsageSnapshotStatePanels
          snapshot={{ ...snapshot, status: 'ready', payload_generation: 2 }}
          isLoading={false}
          onRefresh={() => {}}
        />
      </I18nextProvider>
    )
    expect(
      screen.queryByText('Preparing usage statistics...')
    ).not.toBeInTheDocument()
    expect(screen.queryByText('Previous results')).not.toBeInTheDocument()
    expect(screen.getByText('alice')).toBeVisible()
    expect(view.container.querySelector('[data-slot="skeleton"]')).toBeNull()
  })

  test('failed state surfaces the server reason and retry reruns the query', async () => {
    const onRefresh = vi.fn()
    await renderPanel({
      snapshot: {
        status: 'failed',
        snapshot_id: 1,
        revision: 1,
        payload_generation: 0,
        range_start: 0,
        range_end: 60,
        requested_end: 60,
        slice_done: 0,
        slice_total: 0,
        failure_kind: 'data',
        error: 'source logs cleared',
      },
      onRefresh,
    })
    expect(screen.getByText(/Usage statistics failed/)).toBeTruthy()
    expect(screen.getByText(/source logs cleared/)).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    await waitFor(() => expect(onRefresh).toHaveBeenCalledTimes(1))
  })

  test('ready state renders every region from the same payload', async () => {
    await renderPanel({
      snapshot: {
        status: 'ready',
        snapshot_id: 1,
        revision: 1,
        payload_generation: 1,
        range_start: 0,
        range_end: 60,
        requested_end: 60,
        slice_done: 2,
        slice_total: 2,
        payload: {
          total_quota: 350,
          request_count: 3,
          prompt_tokens: 70,
          completion_tokens: 43,
          member_count: 2,
          active_member_count: 2,
          usage_user_count: 2,
          trend: [
            {
              period: '2026-10-02',
              total_quota: 350,
              request_count: 3,
              prompt_tokens: 70,
              completion_tokens: 43,
            },
          ],
          members: [
            {
              user_id: 8,
              username: 'alice',
              request_count: 3,
              total_quota: 350,
            },
          ],
          models: [
            {
              model_name: 'gpt-4',
              request_count: 3,
              total_quota: 350,
            },
          ],
          channels: [
            {
              channel_id: 1,
              request_count: 3,
              total_quota: 350,
            },
          ],
          models_truncated: false,
          channels_truncated: false,
          covered_until: 60,
          range_start: 0,
          range_end: 60,
          generated_at: 61,
          calculation_version: 1,
        },
      },
    })
    expect(screen.getByText('Usage trend')).toBeTruthy()
    expect(screen.getByText('Model usage')).toBeTruthy()
    expect(screen.getByText('Channel usage')).toBeTruthy()
    expect(screen.getByText('alice')).toBeTruthy()
    expect(screen.getByText('gpt-4')).toBeTruthy()
    expect(screen.getByText('2026-10-02')).toBeTruthy()
    assert.equal(screen.queryByText('Previous results'), null)
  })

  test('queued state without payload explains the wait instead of showing zeros', async () => {
    await renderPanel({
      snapshot: {
        status: 'queued',
        snapshot_id: 0,
        revision: 0,
        payload_generation: 0,
        range_start: 0,
        range_end: 60,
        requested_end: 0,
        slice_done: 0,
        slice_total: 0,
        queue_reason: 'building capacity reached',
      },
    })
    expect(screen.getByText('building capacity reached')).toBeTruthy()
    assert.equal(screen.queryByText('Consumption amount'), null)
  })

  test('loading state without data renders the loading placeholder', async () => {
    const view = await renderPanel({ isLoading: true })
    expect(screen.getByText('Loading usage statistics...')).toBeVisible()
    expect(
      view.container.querySelector('[data-slot="skeleton"]')
    ).toBeInTheDocument()
  })
})
