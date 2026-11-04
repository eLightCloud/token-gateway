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
import { useNavigate, useSearch } from '@tanstack/react-router'
import { useRef, type Dispatch, type SetStateAction } from 'react'
import { z } from 'zod'

// Filters and snapshot cursors survive refresh and browser back navigation.
export const resellerSearchSchema = z.object({
  search: z.string().max(100).optional().catch(undefined),
  activation: z.string().optional().catch(undefined),
  service_status: z.string().optional().catch(undefined),
  request_id: z.string().max(64).optional().catch(undefined),
  model: z.string().optional().catch(undefined),
  kind: z.string().optional().catch(undefined),
  posting: z.string().optional().catch(undefined),
  customer: z.string().optional().catch(undefined),
  start_date: z
    .string()
    .regex(/^\d{4}-\d{2}-\d{2}$/)
    .optional()
    .catch(undefined),
  end_date: z
    .string()
    .regex(/^\d{4}-\d{2}-\d{2}$/)
    .optional()
    .catch(undefined),
  after: z.coerce.number().int().nonnegative().optional().catch(undefined),
  cutoff: z.coerce.number().int().positive().optional().catch(undefined),
  projection_revision: z.coerce
    .number()
    .int()
    .nonnegative()
    .optional()
    .catch(undefined),
})

type FilterKey = keyof z.infer<typeof resellerSearchSchema>
export function useResellerFilter<T extends string | number | undefined>(
  key: FilterKey,
  initial: T
): [T, Dispatch<SetStateAction<T>>] {
  const search = useSearch({ strict: false }) as Record<string, unknown>
  const navigate = useNavigate()
  const fallback = useRef(initial)
  const value = (search[key] ?? fallback.current) as T
  return [
    value,
    (next) => {
      void navigate({
        to: '.',
        replace: true,
        search: (previous) => ({
          ...previous,
          [key]:
            typeof next === 'function'
              ? next(
                  (previous[key as keyof typeof previous] ??
                    fallback.current) as T
                )
              : next,
        }),
      })
    },
  ]
}
