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
const BEIJING_OFFSET_SECONDS = 8 * 60 * 60

// Date#getTimezoneOffset uses UTC - local time, so Beijing (UTC+8) is -480.
export const BEIJING_TIMEZONE_OFFSET_MINUTES = -8 * 60

export function beijingDateBoundaryToUnix(
  value: string,
  endOfDay: boolean
): number | undefined {
  const match = /^(\d{4})-(\d{2})-(\d{2})$/.exec(value)
  if (!match) return undefined

  const year = Number(match[1])
  const month = Number(match[2])
  const day = Number(match[3])
  const utcDate = new Date(Date.UTC(year, month - 1, day))
  const isValidDate =
    utcDate.getUTCFullYear() === year &&
    utcDate.getUTCMonth() === month - 1 &&
    utcDate.getUTCDate() === day
  if (!isValidDate) return undefined

  const boundary = endOfDay
    ? Date.UTC(year, month - 1, day + 1) / 1000 - BEIJING_OFFSET_SECONDS - 1
    : Date.UTC(year, month - 1, day) / 1000 - BEIJING_OFFSET_SECONDS
  return Number.isFinite(boundary) ? boundary : undefined
}

export function unixTimestampToBeijingDateInput(timestamp: number): string {
  if (!timestamp || !Number.isFinite(timestamp)) return ''
  return new Date((timestamp + BEIJING_OFFSET_SECONDS) * 1000)
    .toISOString()
    .slice(0, 10)
}

export function formatTimestampInBeijingTime(timestamp?: number): string {
  if (!timestamp || timestamp === -1 || !Number.isFinite(timestamp)) return '-'
  return new Date((timestamp + BEIJING_OFFSET_SECONDS) * 1000)
    .toISOString()
    .slice(0, 19)
    .replace('T', ' ')
}

export function getBeijingMonthUnixRange(timestamp = Date.now()): {
  startTimestamp: number
  endTimestamp: number
} {
  const beijingNow = new Date(timestamp + BEIJING_OFFSET_SECONDS * 1000)
  const year = beijingNow.getUTCFullYear()
  const month = beijingNow.getUTCMonth()
  const startTimestamp =
    Date.UTC(year, month, 1) / 1000 - BEIJING_OFFSET_SECONDS
  const nextMonthTimestamp =
    Date.UTC(year, month + 1, 1) / 1000 - BEIJING_OFFSET_SECONDS
  return {
    startTimestamp,
    endTimestamp: nextMonthTimestamp - 1,
  }
}

export function getBeijingMonthDateRange(
  timestamp = Date.now(),
  monthOffset = 0
): {
  startDate: string
  endDate: string
  effectiveMonth: string
} {
  const beijingNow = new Date(timestamp + BEIJING_OFFSET_SECONDS * 1000)
  const year = beijingNow.getUTCFullYear()
  const month = beijingNow.getUTCMonth() + monthOffset
  const startDate = new Date(Date.UTC(year, month, 1))
    .toISOString()
    .slice(0, 10)
  const endDate = new Date(Date.UTC(year, month + 1, 0))
    .toISOString()
    .slice(0, 10)
  return {
    startDate,
    endDate,
    effectiveMonth: startDate.slice(0, 7),
  }
}
