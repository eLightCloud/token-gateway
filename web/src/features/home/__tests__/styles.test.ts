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

const mainSource = readFileSync(
  new URL('../../../main.tsx', import.meta.url),
  'utf8'
)
const localStyles = readFileSync(
  new URL('../../../styles/local.css', import.meta.url),
  'utf8'
)
const homeStyles = readFileSync(
  new URL('../../../styles/lighting-home.css', import.meta.url),
  'utf8'
)

describe('Lighting homepage style entry', () => {
  test('loads local styles after the upstream global stylesheet', () => {
    const upstreamIndex = mainSource.indexOf("import './styles/index.css'")
    const localIndex = mainSource.indexOf("import './styles/local.css'")

    assert.ok(upstreamIndex >= 0)
    assert.ok(localIndex > upstreamIndex)
    assert.match(localStyles, /@import ['"]\.\/lighting-home\.css['"];/)
  })

  test('keeps the layout selectors required by the local homepage', () => {
    for (const selector of [
      '.home-skip-link',
      '.home-page',
      '.home-shell',
      '.home-hero',
      "[data-public-header-variant='reference']",
    ]) {
      assert.ok(homeStyles.includes(selector), `missing ${selector}`)
    }
  })
})
