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
const RECOVERY_KEY = 'newapi:chunk-load-recovery'

// A deployment can remove chunks still referenced by an open tab. Only retry
// known module-load failures, at most once per tab session. Persistent failures
// must retain the existing error page rather than start a reload loop.
export function recoverFromChunkLoadError(error: unknown): void {
  if (!(error instanceof Error)) return
  const isChunkError =
    error.name === 'ChunkLoadError' ||
    (error instanceof TypeError &&
      /Failed to fetch dynamically imported module|Importing a module script failed|error loading dynamically imported module/i.test(
        error.message
      ))
  if (!isChunkError) return

  try {
    if (window.sessionStorage.getItem(RECOVERY_KEY)) return
    window.sessionStorage.setItem(RECOVERY_KEY, '1')
    window.location.reload()
  } catch {
    // Without persistent tab storage a reload could repeat indefinitely.
  }
}
