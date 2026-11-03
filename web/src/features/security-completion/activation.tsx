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
import { zodResolver } from '@hookform/resolvers/zod'
import { useMutation } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { useState } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { z } from 'zod'

import { PasswordInput } from '@/components/password-input'
import { Button } from '@/components/ui/button'
import {
  Form,
  FormControl,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { encryptPassword } from '@/features/auth/lib/password-encryption'
import type { AccountSecurityResult } from '@/features/profile/types'
import { api } from '@/lib/api'
import { accountPasswordSchema } from '@/lib/password-policy'
import {
  AuthOperationError,
  authRequestOptions,
  authResult,
} from '@/lib/secure-verification'

import { confirmSecurityEmail } from './api'

const schema = z.object({
  password: accountPasswordSchema,
})

export function AccountActivation() {
  const { t } = useTranslation()
  const [link] = useState(() => {
    const params = new URLSearchParams(window.location.hash.slice(1))
    window.history.replaceState(null, '', window.location.pathname)
    return {
      token: params.get('token') ?? '',
      user_id: Number(params.get('user_id')),
      purpose: params.get('purpose'),
    }
  })
  const form = useForm<z.infer<typeof schema>>({
    resolver: zodResolver(schema),
    defaultValues: { password: '' },
  })
  const complete = useMutation({
    mutationFn: async (password: string) => {
      if (link.purpose === 'security_email') {
        return confirmSecurityEmail(link.token)
      }
      if (link.purpose === 'security_email_change') {
        return authResult<AccountSecurityResult>(
          api.post('/api/security/email-change/complete', link, {
            ...authRequestOptions,
            singleUseAuthorization: true,
          })
        )
      }
      const encrypted = await encryptPassword(password)
      return authResult<AccountSecurityResult>(
        api.post(
          link.purpose === 'security_password_reset'
            ? '/api/security/password/reset'
            : '/api/security/activation',
          { ...link, ...encrypted },
          { ...authRequestOptions, singleUseAuthorization: true }
        )
      )
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
        (link.purpose === 'security_email' ||
        link.purpose === 'security_email_change' ? (
          <Button
            disabled={complete.isPending}
            onClick={() => complete.mutate('')}
          >
            {t('Verify Email')}
          </Button>
        ) : (
          <Form {...form}>
            <form
              className='space-y-4'
              onSubmit={form.handleSubmit((values) =>
                complete.mutate(values.password)
              )}
            >
              <FormField
                control={form.control}
                name='password'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('New Password')}</FormLabel>
                    <FormControl>
                      <PasswordInput {...field} autoComplete='new-password' />
                    </FormControl>
                    <FormMessage />
                  </FormItem>
                )}
              />
              <Button type='submit' disabled={complete.isPending}>
                {link.purpose === 'security_password_reset'
                  ? t('Reset Password')
                  : t('Activate account')}
              </Button>
            </form>
          </Form>
        ))}
      {complete.isError && (
        <p role='alert'>
          {complete.error instanceof AuthOperationError &&
          complete.error.code !== 'AUTH_FLOW_INVALID'
            ? t(complete.error.message)
            : t('This link is invalid or has expired. Request a new link.')}
        </p>
      )}
      {complete.isSuccess && (
        <p role='status'>
          {link.purpose === 'security_password_reset'
            ? t(
                'Password reset completed. Sign in with your password and authenticator.'
              )
            : t('Email verified. Continue with security setup.')}
        </p>
      )}
      {complete.data?.notification_warning && (
        <p role='alert'>
          {t(
            'The change succeeded, but the notification email could not be sent.'
          )}
        </p>
      )}
      {link.purpose === 'security_email' ? (
        <Link to='/security-setup'>{t('Continue')}</Link>
      ) : (
        <Link to='/sign-in'>{t('Sign In')}</Link>
      )}
    </main>
  )
}
