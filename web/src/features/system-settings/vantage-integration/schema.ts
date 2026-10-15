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
import * as z from 'zod'

export function parseVantageIPv4List(value: string) {
  return [...new Set(value.split(/[\s,]+/).filter(Boolean))]
}

export function createVantageIntegrationSchema(
  t: (key: string) => string,
  secretConfigured: boolean
) {
  return z
    .object({
      enabled: z.boolean(),
      allowed_ipv4: z
        .string()
        .refine(
          (value) =>
            parseVantageIPv4List(value).every(
              (ip) => z.ipv4().safeParse(ip).success
            ),
          t('Only complete IPv4 addresses are allowed.')
        ),
      secret: z
        .string()
        .refine(
          (value) =>
            value === '' || new TextEncoder().encode(value).length >= 32,
          t('The application secret must contain at least 32 bytes.')
        ),
    })
    .superRefine((values, context) => {
      if (!values.enabled) return
      if (parseVantageIPv4List(values.allowed_ipv4).length === 0) {
        context.addIssue({
          code: 'custom',
          path: ['allowed_ipv4'],
          message: t('An IPv4 allowlist is required to enable integration.'),
        })
      }
      if (!secretConfigured && values.secret === '') {
        context.addIssue({
          code: 'custom',
          path: ['secret'],
          message: t('Set an application secret before enabling integration.'),
        })
      }
    })
}
