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
import { Link, useNavigate } from '@tanstack/react-router'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import { encryptPassword } from '@/features/auth/lib/password-encryption'
import type { AccountSecurityResult } from '@/features/profile/types'
import { api } from '@/lib/api'
import {
  AuthOperationError,
  authRequestOptions,
  authResult,
} from '@/lib/secure-verification'

import { confirmSecurityEmail } from './api'
import { PasswordSetupForm } from './password-setup-form'

export function AccountActivation() {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const [link] = useState(() => {
    const params = new URLSearchParams(window.location.hash.slice(1))
    window.history.replaceState(null, '', window.location.pathname)
    return {
      token: params.get('token') ?? '',
      user_id: Number(params.get('user_id')),
      purpose: params.get('purpose'),
    }
  })
  const confirmsEmail =
    link.purpose === 'security_email' ||
    link.purpose === 'security_email_change'
  const resetsPassword = link.purpose === 'security_password_reset'
  const complete = useMutation({
    meta: { errorToast: false },
    gcTime: 0,
    mutationFn: async (password: string) => {
      if (link.purpose === 'security_email') {
        return confirmSecurityEmail(link.token)
      }
      if (link.purpose === 'security_email_change') {
        return authResult<AccountSecurityResult>(
          api.post('/api/security/email-change/complete', link, {
            ...authRequestOptions,
            skipAuthRefresh: true,
          })
        )
      }
      const encrypted = await encryptPassword(password)
      return authResult<AccountSecurityResult>(
        api.post(
          resetsPassword
            ? '/api/security/password/reset'
            : '/api/security/activation',
          { ...link, ...encrypted },
          { ...authRequestOptions, skipAuthRefresh: true }
        )
      )
    },
    onSuccess: async (result) => {
      if (confirmsEmail) return

      toast.success(
        resetsPassword
          ? t(
              'Password reset completed. Sign in with your password and authenticator.'
            )
          : t('Password set successfully')
      )
      if (result?.notification_warning) {
        toast.warning(
          t(
            'The change succeeded, but the notification email could not be sent.'
          )
        )
      }
      await navigate({
        to: '/sign-in',
        search: { passwordSetupComplete: true },
        replace: true,
      })
    },
  })
  return (
    <main className='mx-auto flex min-h-svh w-full max-w-lg flex-col justify-center gap-4 p-6'>
      <h1 className='text-2xl font-semibold'>
        {t('Complete account security setup')}
      </h1>
      {!link.token && (
        <p role='alert'>
          {t('This link is invalid or has expired. Request a new link.')}
        </p>
      )}
      {link.token &&
        !complete.isSuccess &&
        (confirmsEmail ? (
          <Button
            disabled={complete.isPending}
            onClick={() => complete.mutate('')}
          >
            {t('Verify Email')}
          </Button>
        ) : (
          <PasswordSetupForm
            pending={complete.isPending}
            onConfirm={complete.mutate}
          />
        ))}
      {complete.isError && (
        <p role='alert'>
          {complete.error instanceof AuthOperationError &&
          complete.error.code !== 'AUTH_FLOW_INVALID'
            ? t(complete.error.message)
            : t('This link is invalid or has expired. Request a new link.')}
        </p>
      )}
      {confirmsEmail && complete.isSuccess && (
        <p role='status'>
          {t('Email verified. Continue with security setup.')}
        </p>
      )}
      {confirmsEmail && complete.data?.notification_warning && (
        <p role='alert'>
          {t(
            'The change succeeded, but the notification email could not be sent.'
          )}
        </p>
      )}
      {link.purpose === 'security_email' && (
        <Link to='/security-setup'>{t('Continue')}</Link>
      )}
      {link.purpose === 'security_email_change' && (
        <Link to='/sign-in'>{t('Sign In')}</Link>
      )}
    </main>
  )
}
