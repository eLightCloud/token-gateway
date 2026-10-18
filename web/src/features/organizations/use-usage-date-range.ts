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
import { useState } from 'react'

import {
  beijingDateBoundaryToUnix,
  unixTimestampToBeijingDateInput,
} from './beijing-time'

export function useUsageDateRange(options?: {
  initialRange?: { startDate: string; endDate: string }
  required?: boolean
}) {
  const [startDate, setStartDate] = useState(
    options?.initialRange?.startDate ?? ''
  )
  const [endDate, setEndDate] = useState(options?.initialRange?.endDate ?? '')
  const latestDate = unixTimestampToBeijingDateInput(Date.now() / 1000)
  let dateRangeError: 'invalid' | 'reversed' | 'future' | undefined
  if (
    (options?.required && (!startDate || !endDate)) ||
    (startDate && beijingDateBoundaryToUnix(startDate, false) === undefined) ||
    (endDate && beijingDateBoundaryToUnix(endDate, false) === undefined)
  ) {
    dateRangeError = 'invalid'
  } else if (startDate && endDate && startDate > endDate) {
    dateRangeError = 'reversed'
  } else if (startDate > latestDate || endDate > latestDate) {
    dateRangeError = 'future'
  }
  return {
    startDate,
    setStartDate,
    endDate,
    setEndDate,
    latestDate,
    dateRangeError,
    isDateRangeValid: dateRangeError === undefined,
  }
}
