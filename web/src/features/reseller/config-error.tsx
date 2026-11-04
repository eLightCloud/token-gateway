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
import { useMutation } from '@tanstack/react-query'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { Button } from '@/components/ui/button'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber } from '@/lib/format'
import { getServerErrorSources } from '@/lib/server-error-message'

export function ResellerConfigError(props: {
  error: unknown
  onReload: () => Promise<void>
}) {
  const { t, i18n } = useTranslation()
  const [confirmOpen, setConfirmOpen] = useState(false)
  const reload = useMutation({
    mutationFn: props.onReload,
    meta: { errorToast: false },
    onSuccess: () => setConfirmOpen(false),
  })
  if (!props.error) return null
  const conflict = getServerErrorSources(props.error).find(
    (source) => source.code === 'RESELLER_REVISION_CONFLICT'
  )
  const data = conflict?.data
  const revision =
    data && typeof data === 'object' && 'revision' in data
      ? data.revision
      : undefined
  const knownRevision =
    typeof revision === 'number' &&
    Number.isSafeInteger(revision) &&
    revision >= 0
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  return (
    <>
      <div role='alert' className='space-y-2'>
        <p>
          {knownRevision
            ? t(
                'Configuration changed on the server (revision {{revision}}). Your draft is preserved.',
                { revision: formatNumber(revision, locale) }
              )
            : t(
                'Save failed or configuration changed. Refresh and compare before saving again.'
              )}
        </p>
        {conflict && (
          <Button variant='outline' onClick={() => setConfirmOpen(true)}>
            {t('Reload configuration')}
          </Button>
        )}
      </div>
      <ConfirmDialog
        open={confirmOpen}
        onOpenChange={(open) => {
          if (!reload.isPending) {
            setConfirmOpen(open)
            if (!open) reload.reset()
          }
        }}
        title={t('Unsaved changes')}
        desc={t(
          'Reloading will discard your unsaved changes and load the latest configuration. Continue?'
        )}
        confirmText={t('Reload configuration')}
        destructive
        isLoading={reload.isPending}
        handleConfirm={() => reload.mutate()}
      >
        {reload.isError && (
          <p role='alert'>
            {t('Failed to reload configuration. Your draft is preserved.')}
          </p>
        )}
      </ConfirmDialog>
    </>
  )
}
