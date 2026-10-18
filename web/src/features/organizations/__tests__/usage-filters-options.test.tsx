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

import { QueryClient } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import { createInstance } from 'i18next'
import { I18nextProvider } from 'react-i18next'
import { describe, test, expect } from 'vitest'

import en from '@/i18n/locales/en.json'

import { organizationKeys } from '../api'
import { UsageFilters } from '../index'
import type { OrganizationBillingFilterOptions } from '../types'
import {
  dateToUnix,
  unixEndOfDate,
  useUsageFilters,
} from '../use-usage-filters'

function FiltersHarness(props: {
  options?: OrganizationBillingFilterOptions
  optionsLoading?: boolean
}) {
  const filters = useUsageFilters()
  return (
    <UsageFilters
      filters={filters}
      options={props.options}
      optionsLoading={props.optionsLoading}
      showMemberFilter
      showChannelFilter
      onRefresh={() => {}}
    />
  )
}

async function renderFilters(props: {
  options?: OrganizationBillingFilterOptions
  optionsLoading?: boolean
}) {
  const i18n = createInstance()
  await i18n.init({ lng: 'en', resources: { en: { translation: en } } })
  return render(
    <I18nextProvider i18n={i18n}>
      <FiltersHarness {...props} />
    </I18nextProvider>
  )
}

describe('usage filters options directory', () => {
  test('switching organizations does not reuse another organization usage cache', () => {
    const client = new QueryClient()
    client.setQueryData(organizationKeys.usageSnapshot({}, 1), {
      total_quota: 123,
    })
    expect(
      client.getQueryData(organizationKeys.usageSnapshot({}, 2))
    ).toBeUndefined()
    client.clear()
  })

  test('a selected end date includes its last second using the next midnight boundary', () => {
    expect(unixEndOfDate('2026-10-17')).toBe(dateToUnix('2026-10-18'))
  })

  test('selects stay disabled with a preparing hint while the directory builds', async () => {
    await renderFilters({ optionsLoading: true })
    const userSelect = screen
      .getAllByRole('combobox')
      .find((element) => element.textContent?.includes('Preparing')) as
      | HTMLSelectElement
      | undefined
    expect(userSelect).toBeTruthy()
    assert.equal(userSelect?.disabled, true)
  })

  test('ready directory options render as enabled choices', async () => {
    await renderFilters({
      options: {
        members: [
          { user_id: 7, username: 'alice', request_count: 1, total_quota: 5 },
        ],
        models: [{ model_name: 'gpt-4', request_count: 1, total_quota: 5 }],
        channels: [
          {
            channel_id: 3,
            channel_name: 'main',
            request_count: 1,
            total_quota: 5,
          },
        ],
      },
    })
    const selects = screen.getAllByRole('combobox') as HTMLSelectElement[]
    assert.equal(selects.length, 3)
    for (const select of selects) {
      assert.equal(select.disabled, false)
    }
    expect(screen.getByText('alice')).toBeTruthy()
    expect(screen.getByText('gpt-4')).toBeTruthy()
    expect(screen.getByText('main')).toBeTruthy()
  })
})
