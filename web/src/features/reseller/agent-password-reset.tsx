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
import { toast } from 'sonner'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { Button } from '@/components/ui/button'
import { handleServerError } from '@/lib/handle-server-error'

import { resellerRequest } from './api'

export function ResellerAgentPasswordReset(props: {
  accountPath: string
  disabled?: boolean
}) {
  const { t } = useTranslation()
  const [open, setOpen] = useState(false)
  const reset = useMutation({
    meta: { errorToast: false },
    retry: false,
    mutationFn: () =>
      resellerRequest(`${props.accountPath}/password-reset`, 'post', {}),
    onSuccess: () => {
      setOpen(false)
      toast.success(t('Password reset email submitted to the mail server.'))
    },
    onError: (error) =>
      handleServerError(error, t('Failed to send password reset email')),
  })

  return (
    <>
      <Button
        variant='outline'
        size='sm'
        disabled={props.disabled || reset.isPending}
        onClick={() => setOpen(true)}
      >
        {t('Reset Password')}
      </Button>
      <ConfirmDialog
        open={open}
        onOpenChange={setOpen}
        title={t('Reset Password')}
        desc={t(
          'A password reset link will be sent to the current email address.'
        )}
        confirmText={reset.isPending ? t('Sending...') : t('Send')}
        isLoading={reset.isPending}
        handleConfirm={() => reset.mutate()}
      />
    </>
  )
}
