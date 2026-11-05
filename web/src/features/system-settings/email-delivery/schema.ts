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
import { z } from 'zod'

import type { EmailSettings, EmailUpdate } from './types'

export function emailFormDefaults(settings: EmailSettings): EmailUpdate {
  return {
    intent: {
      expected_revision: settings.revision,
      options: structuredClone(settings.options),
      legacy_password_action: 'keep',
      client_secret_action: 'keep',
    },
    legacy_password: '',
    client_secret: '',
  }
}
export function createEmailSchema(
  t: (key: string) => string,
  settings: EmailSettings
) {
  const guid = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i
  return z
    .object({
      intent: z.object({
        expected_revision: z
          .number()
          .int()
          .nonnegative()
          .max(Number.MAX_SAFE_INTEGER - 1),
        options: z.object({
          auth_mode: z.enum(['legacy', 'microsoft_oauth2_app']),
          legacy: z.object({
            host: z.string().trim(),
            port: z.number(),
            account: z.string().trim(),
            from: z.string().trim(),
            ssl: z.boolean(),
            starttls: z.boolean(),
            insecure_skip_verify: z.boolean(),
            force_login: z.boolean(),
          }),
          microsoft: z.object({
            tenant_id: z.string().trim(),
            client_id: z.string().trim(),
            mailbox: z.string().trim(),
          }),
        }),
        legacy_password_action: z.enum(['keep', 'replace', 'clear']),
        client_secret_action: z.enum(['keep', 'replace', 'clear']),
      }),
      legacy_password: z.string().max(8192),
      client_secret: z.string().max(8192),
    })
    .superRefine((values, ctx) => {
      const options = values.intent.options
      if (options.auth_mode === 'microsoft_oauth2_app') {
        for (const field of ['tenant_id', 'client_id'] as const) {
          if (!guid.test(options.microsoft[field])) {
            ctx.addIssue({
              code: 'custom',
              path: ['intent', 'options', 'microsoft', field],
              message: t('Enter a valid Microsoft application or tenant ID'),
            })
          }
        }
        if (!z.email().safeParse(options.microsoft.mailbox).success) {
          ctx.addIssue({
            code: 'custom',
            path: ['intent', 'options', 'microsoft', 'mailbox'],
            message: t('Enter a valid email address'),
          })
        }
        if (
          values.intent.client_secret_action === 'clear' ||
          (values.intent.client_secret_action === 'keep' &&
            !settings.client_secret_configured)
        ) {
          ctx.addIssue({
            code: 'custom',
            path: ['client_secret'],
            message: t(
              'Enter an application secret before enabling Microsoft email'
            ),
          })
        }
      } else {
        if (!options.legacy.host || /[\s/\\:]/.test(options.legacy.host)) {
          ctx.addIssue({
            code: 'custom',
            path: ['intent', 'options', 'legacy', 'host'],
            message: t('Enter a valid SMTP host'),
          })
        }
        if (
          !Number.isInteger(options.legacy.port) ||
          options.legacy.port < 1 ||
          options.legacy.port > 65535
        ) {
          ctx.addIssue({
            code: 'custom',
            path: ['intent', 'options', 'legacy', 'port'],
            message: t('Enter a port between 1 and 65535'),
          })
        }
        if (
          !z.email().safeParse(options.legacy.from || options.legacy.account)
            .success
        ) {
          ctx.addIssue({
            code: 'custom',
            path: ['intent', 'options', 'legacy', 'from'],
            message: t('Enter a valid email address'),
          })
        }
      }
      const changedApp =
        options.microsoft.tenant_id !== settings.options.microsoft.tenant_id ||
        options.microsoft.client_id !== settings.options.microsoft.client_id
      if (
        changedApp &&
        (options.microsoft.tenant_id || options.microsoft.client_id) &&
        values.intent.client_secret_action !== 'replace'
      ) {
        ctx.addIssue({
          code: 'custom',
          path: ['client_secret'],
          message: t(
            'Enter a new secret when changing the Microsoft application'
          ),
        })
      }
      for (const [field, action] of [
        ['client_secret', values.intent.client_secret_action],
        ['legacy_password', values.intent.legacy_password_action],
      ] as const) {
        if (
          (action === 'replace' && !values[field]) ||
          (action !== 'replace' && values[field]) ||
          values[field].includes(String.fromCharCode(0)) ||
          /[\r\n]/.test(values[field])
        ) {
          ctx.addIssue({
            code: 'custom',
            path: [field],
            message: t('Enter a valid credential or leave blank to keep it'),
          })
        }
        if (
          !settings.storage_key_ready &&
          (action === 'replace' ||
            (action === 'keep' &&
              (settings.client_secret_configured ||
                settings.legacy_password_configured)))
        ) {
          ctx.addIssue({
            code: 'custom',
            path: [field],
            message: t(
              'Configure EMAIL_DELIVERY_STORAGE_KEY on every server before saving credentials'
            ),
          })
        }
      }
    })
}
