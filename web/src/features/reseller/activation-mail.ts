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
import { useMutation, useMutationState } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { handleServerError } from '@/lib/handle-server-error'
import { getServerErrorMessage } from '@/lib/server-error-message'

import { resellerKey, resellerPath, resellerRequest } from './api'

export function sendResellerActivationEmail(
  admin: boolean,
  userID: number,
  email?: string
): Promise<null> {
  return resellerRequest(
    `${resellerPath(admin)}/${userID}/activation`,
    'post',
    email ? { email } : {}
  )
}

export function useResellerActivationMail(
  admin: boolean,
  viewer: number | undefined
) {
  const { t } = useTranslation()
  const mutationKey = resellerKey(viewer, 'activation-email', admin)
  const { mutate } = useMutation({
    mutationKey,
    meta: { errorToast: false },
    retry: false,
    mutationFn: (input: { userID: number; afterCreate?: boolean }) =>
      sendResellerActivationEmail(admin, input.userID),
    onSuccess: () =>
      toast.success(
        t('Account information email submitted to the mail server.')
      ),
    onError: (error, input) => {
      const fallback = t('Failed to send account information email')
      if (!admin && input.afterCreate) {
        const title = t(
          'Customer created, but the account email could not be sent.'
        )
        handleServerError(error, title, {
          title,
          description: getServerErrorMessage(error, fallback),
        })
        return
      }
      handleServerError(error, fallback)
    },
  })
  const sending = useMutationState({
    filters: { mutationKey, exact: true, status: 'pending' },
    select: (mutation) =>
      (mutation.state.variables as { userID: number } | undefined)?.userID,
  })
  return { sendActivationMail: mutate, sending }
}
