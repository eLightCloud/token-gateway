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
import { readFileSync } from 'node:fs'
import { describe, test } from 'node:test'

import type { TFunction } from 'i18next'

import { organizationInvoiceCategoryName } from '../invoice-category'

const translate = ((key: string) => `translated:${key}`) as TFunction

describe('organization invoice category labels', () => {
  test('maps every standard category key to its localized label key', () => {
    const cases = [
      ['claude', 'Claude'],
      ['gpt', 'GPT'],
      ['gemini', 'Gemini'],
      ['deepseek', 'Deepseek'],
      ['minimax', 'Minimax (Alibaba Cloud)'],
      ['kimi', 'Kimi (Alibaba Cloud)'],
      ['glm', 'GLM (Alibaba Cloud)'],
      ['qwen', 'Qwen (Alibaba Cloud)'],
      ['vector', 'Vector'],
    ] as const

    for (const [categoryKey, labelKey] of cases) {
      assert.equal(
        organizationInvoiceCategoryName(
          categoryKey,
          'server category name',
          false,
          translate
        ),
        `translated:${labelKey}`
      )
    }
  })

  test('keeps server names for fallback and unknown categories', () => {
    assert.equal(
      organizationInvoiceCategoryName(
        'model.hash',
        'custom-model-v1',
        true,
        translate
      ),
      'custom-model-v1'
    )
    assert.equal(
      organizationInvoiceCategoryName(
        'future-category',
        'Future category',
        false,
        translate
      ),
      'Future category'
    )
  })

  test('uses the confirmed Simplified Chinese business labels', () => {
    const locale = JSON.parse(
      readFileSync(
        new URL('../../../i18n/locales/zh.json', import.meta.url),
        'utf8'
      )
    ) as { translation: Record<string, string> }

    assert.equal(
      locale.translation['Minimax (Alibaba Cloud)'],
      'Minimax（阿里云）'
    )
    assert.equal(locale.translation['Kimi (Alibaba Cloud)'], 'Kimi（阿里云）')
    assert.equal(locale.translation['GLM (Alibaba Cloud)'], 'GLM（阿里云）')
    assert.equal(locale.translation['Qwen (Alibaba Cloud)'], 'Qwen（阿里云）')
    assert.equal(locale.translation.Vector, '向量')
  })
})
