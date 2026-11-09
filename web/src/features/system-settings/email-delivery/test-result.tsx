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
import { useTranslation } from 'react-i18next'

import { toIntlLocale } from '@/i18n/languages'
import { formatNumber } from '@/lib/format'

import type { EmailReport } from './types'

export function EmailTestResult(props: {
  report: EmailReport
  revision: number
}) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const labels = {
    config: t('Configuration check'),
    token: t('Microsoft token'),
    connect_tls: t('SMTP connection and TLS'),
    auth: t('SMTP authentication'),
    submit: t('Mail submission'),
    graph_submit: t('Microsoft Graph submission'),
  }
  const states = {
    passed: t('Passed'),
    failed: t('Failed'),
    skipped: t('Not applicable or not started'),
    unknown: t('Unknown'),
  }
  let message = t(
    'Email test failed. Check the failed stage and provider settings.'
  )
  if (props.report.code === 'EMAIL_ACCEPTED') {
    message = t(
      'The mail provider accepted the email. Check the recipient inbox for delivery.'
    )
  }
  if (props.report.code === 'EMAIL_DELIVERY_UNKNOWN') {
    message = t(
      'Submission result is uncertain. Check the inbox before trying again.'
    )
  }
  if (props.report.reason === 'GRAPH_ACCESS_DENIED') {
    message = t(
      'Microsoft Graph denied access. Check Mail.Send permission and access to the sender mailbox.'
    )
  }
  return (
    <div
      role='status'
      aria-live='polite'
      className='bg-muted/20 space-y-3 rounded-lg border p-4 text-sm'
    >
      <p>{message}</p>
      {props.report.revision !== props.revision && (
        <p>
          {t(
            'This result belongs to an older configuration. Test again after saving.'
          )}
        </p>
      )}
      <p>
        {t('Configuration version')}:{' '}
        {formatNumber(props.report.revision, locale)} ·{' '}
        {new Date(props.report.timestamp * 1000).toLocaleString(locale)}
      </p>
      <ul className='space-y-1'>
        {props.report.stages.map((stage) => (
          <li key={stage.name}>
            {labels[stage.name]}: {states[stage.status]}
          </li>
        ))}
      </ul>
      <p className='break-all'>
        {t('Request ID')}: {props.report.request_id}
      </p>
    </div>
  )
}
