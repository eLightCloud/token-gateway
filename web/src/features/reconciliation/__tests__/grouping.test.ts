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
import { describe, test } from 'node:test'

import { billDetailBreakdownDimension, filtersForBillGroup } from '../grouping'
import type { BillDimension, BillFilters, BillGroupRow } from '../types'

function filters(dimension: BillDimension): BillFilters {
  return {
    startTimestamp: 100,
    endTimestamp: 200,
    perspective: dimension.startsWith('user') ? 'customer' : 'upstream',
    dimension,
    billType: 'all',
    page: 3,
    pageSize: 20,
  }
}

describe('token bill composite grouping', () => {
  test('keeps customer identity when opening a customer-model group', () => {
    const result = filtersForBillGroup(filters('user_model'), {
      key: '11:gpt-test',
      label: 'alice',
      user_id: 11,
      model_name: 'gpt-test',
      record_count: 1,
      prompt_tokens: 10,
      completion_tokens: 20,
      quota: 100,
    })

    assert.equal(result.userId, 11)
    assert.equal(result.modelName, 'gpt-test')
    assert.equal(result.page, 1)
  })

  test('keeps channel identity when opening a channel-model group', () => {
    const result = filtersForBillGroup(filters('channel_model'), {
      key: '7:gpt-test',
      label: 'Primary Channel',
      channel_id: 7,
      model_name: 'gpt-test',
      record_count: 1,
      prompt_tokens: 10,
      completion_tokens: 20,
      quota: 100,
    })

    assert.equal(result.channelId, 7)
    assert.equal(result.modelName, 'gpt-test')
  })

  test('keeps both current API address and channel when opening upstream details', () => {
    const upstreamFilters = {
      ...filters('upstream_channel'),
      perspective: 'api_address' as const,
    }
    const row: BillGroupRow = {
      key: '7:api',
      label: 'https://api.example.com/v1',
      api_address: 'https://api.example.com/v1',
      channel_id: 7,
      record_count: 1,
      prompt_tokens: 10,
      completion_tokens: 20,
      quota: 100,
    }

    const result = filtersForBillGroup(upstreamFilters, row)

    assert.equal(result.apiAddress, 'https://api.example.com/v1')
    assert.equal(result.channelId, 7)
    assert.equal(
      billDetailBreakdownDimension(result.dimension),
      'upstream_channel_model'
    )
  })

  test('keeps address, channel, and model for an upstream-model group', () => {
    const upstreamFilters = {
      ...filters('upstream_channel_model'),
      perspective: 'api_address' as const,
    }
    const result = filtersForBillGroup(upstreamFilters, {
      key: '7:api:gpt-test',
      label: 'https://api.example.com/v1',
      api_address: 'https://api.example.com/v1',
      channel_id: 7,
      model_name: 'gpt-test',
      record_count: 1,
      prompt_tokens: 10,
      completion_tokens: 20,
      quota: 100,
    })

    assert.equal(result.apiAddress, 'https://api.example.com/v1')
    assert.equal(result.channelId, 7)
    assert.equal(result.modelName, 'gpt-test')
    assert.equal(billDetailBreakdownDimension(result.dimension), undefined)
  })
})
