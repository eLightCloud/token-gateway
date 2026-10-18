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
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { recoverFromChunkLoadError } from '../chunk-load-recovery'

const reload = vi.fn()

beforeEach(() => {
  sessionStorage.clear()
  vi.stubGlobal('window', { sessionStorage, location: { reload } })
})

afterEach(() => {
  vi.unstubAllGlobals()
  sessionStorage.clear()
  reload.mockClear()
})

it('a missing lazy chunk reloads once and a repeated failure keeps the error page', () => {
  const error = Object.assign(new Error('Loading chunk 47565 failed.'), {
    name: 'ChunkLoadError',
  })
  recoverFromChunkLoadError(error)
  recoverFromChunkLoadError(error)
  expect(reload).toHaveBeenCalledTimes(1)
})

it.each([
  new Error('render failed'),
  new TypeError('Cannot read properties of undefined'),
  new TypeError('Failed to fetch'),
  { response: { status: 500 } },
])(
  'ordinary rendering or API errors never trigger automatic reload: %s',
  (error) => {
    recoverFromChunkLoadError(error)
    expect(reload).not.toHaveBeenCalled()
  }
)

it.each([
  'Failed to fetch dynamically imported module: http://localhost/static/js/old.js',
  'Importing a module script failed.',
  'error loading dynamically imported module: http://localhost/static/js/old.js',
])('browser-specific dynamic import failure recovers once: %s', (message) => {
  recoverFromChunkLoadError(new TypeError(message))
  expect(reload).toHaveBeenCalledTimes(1)
})

it('unavailable session storage leaves the error page without risking a reload loop', () => {
  vi.stubGlobal('window', {
    get sessionStorage() {
      throw new Error('storage blocked')
    },
    location: { reload },
  })
  recoverFromChunkLoadError(
    Object.assign(new Error(), { name: 'ChunkLoadError' })
  )
  expect(reload).not.toHaveBeenCalled()
})
