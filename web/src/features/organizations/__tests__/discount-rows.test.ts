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

import {
  hasInvalidDiscountRows,
  isValidDiscountRatio,
  nextDiscountRowKey,
  rowsToPayload,
} from '../discount-rows'

describe('organization discount draft rows', () => {
  test('allocates a key after every server row', () => {
    assert.equal(
      nextDiscountRowKey([
        { key: 1, channelId: 10, ratio: '0.8' },
        { key: 2, channelId: 20, ratio: '0.9' },
      ]),
      3
    )
  })

  test('rejects incomplete and duplicate rows instead of silently dropping them', () => {
    assert.equal(
      hasInvalidDiscountRows([{ key: 1, channelId: 0, ratio: '1.0' }]),
      true
    )
    assert.equal(
      hasInvalidDiscountRows([
        { key: 1, channelId: 10, ratio: '0.8' },
        { key: 2, channelId: 10, ratio: '0.9' },
      ]),
      true
    )
    assert.deepEqual(
      rowsToPayload([{ key: 1, channelId: 10, ratio: ' 0.8 ' }]),
      [{ channel_id: 10, ratio: '0.8' }]
    )
  })

  test('accepts only billing-supported discount ratios', () => {
    for (const ratio of ['0.000001', '0.8', '1', '1.000000']) {
      assert.equal(isValidDiscountRatio(ratio), true, ratio)
    }
    for (const ratio of [
      '',
      'discount',
      '0',
      '-0.1',
      '1.1',
      '0.1234567',
      '1e-1',
    ]) {
      assert.equal(isValidDiscountRatio(ratio), false, ratio)
    }
    assert.equal(
      hasInvalidDiscountRows([{ key: 1, channelId: 10, ratio: 'discount' }]),
      true
    )
  })
})
