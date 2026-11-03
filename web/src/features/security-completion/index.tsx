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
import { useMutation, useQuery } from '@tanstack/react-query'
import { Link, useRouter } from '@tanstack/react-router'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { SignOutDialog } from '@/components/sign-out-dialog'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { EmailBindDialog } from '@/features/security/components/dialogs/email-bind-dialog'
import { TwoFACard } from '@/features/security/components/two-fa-card'
import { useCountdown } from '@/hooks/use-countdown'
import { useAuthStore } from '@/stores/auth-store'

import { getSecurityCompletion, startSecurityEmail } from './api'

// Standalone enrollment has no dashboard/sidebar providers or business queries.
export function SecurityCompletionPage() {
  const { t } = useTranslation()
  const router = useRouter()
  const userID = useAuthStore((s) => s.auth.user?.id)
  const [sent, setSent] = useState(false)
  const resend = useCountdown({ initialSeconds: 60 })
  const [bindEmail, setBindEmail] = useState(false)
  const [signOut, setSignOut] = useState(false)
  const status = useQuery({
    queryKey: ['security-completion', userID],
    queryFn: getSecurityCompletion,
    retry: false,
  })
  const email = useMutation({
    mutationFn: startSecurityEmail,
    onSuccess: () => {
      setSent(true)
      resend.start()
    },
  })

  return (
    <main className='mx-auto flex min-h-svh w-full max-w-xl flex-col justify-center gap-4 p-4 sm:p-8'>
      <h1 className='text-2xl font-semibold'>
        {t('Complete account security setup')}
      </h1>
      {status.isPending && <p role='status'>{t('Loading...')}</p>}
      {status.isError && (
        <div role='alert'>
          <p>{t('Security status is unavailable')}</p>
          <Button onClick={() => void status.refetch()}>{t('Retry')}</Button>
        </div>
      )}
      {status.data && (
        <>
          <Card>
            <CardHeader>
              <CardTitle>{t('Email verification')}</CardTitle>
            </CardHeader>
            <CardContent className='space-y-3'>
              <p className='break-all'>{status.data.email}</p>
              <Button variant='outline' onClick={() => setBindEmail(true)}>
                {t('Change Email')}
              </Button>
              <EmailBindDialog
                open={bindEmail}
                onOpenChange={setBindEmail}
                currentEmail={status.data.email}
                onSuccess={() => void status.refetch()}
              />
              <p>
                {status.data.email_verified
                  ? t('Verified')
                  : t('Verify the email address to continue.')}
              </p>
              {!status.data.email_verified && status.data.email !== '' && (
                <Button
                  disabled={email.isPending || sent}
                  onClick={() => email.mutate()}
                >
                  {t('Send verification email')}
                </Button>
              )}
              {email.isError && (
                <p role='alert'>{t('Failed to send verification email')}</p>
              )}
              {sent && (
                <p role='status'>
                  {t(
                    'Check your email. You can request a new link after one minute.'
                  )}
                </p>
              )}
              {sent && (
                <Button
                  variant='outline'
                  disabled={email.isPending || resend.isActive}
                  onClick={() => email.mutate()}
                >
                  {resend.isActive
                    ? t('Resend in {{seconds}}s', {
                        seconds: resend.secondsLeft,
                      })
                    : t('Resend')}
                </Button>
              )}
            </CardContent>
          </Card>
          {status.data.email_verified && <TwoFACard loading={false} required />}
          <Button
            variant='outline'
            onClick={() => {
              void status.refetch()
              void router.invalidate()
            }}
          >
            {t('Refresh')}
          </Button>
          {status.data.complete && (
            <p role='status'>{t('Account security setup is complete.')}</p>
          )}
          {status.data.complete && status.data.kind === '' && (
            <Link to='/dashboard'>{t('Continue')}</Link>
          )}
          {status.data.kind !== '' && status.data.complete && (
            <p>
              {t(
                'Reseller business access will open after billing verification.'
              )}
            </p>
          )}
        </>
      )}
      <Button variant='ghost' onClick={() => setSignOut(true)}>
        {t('Sign out')}
      </Button>
      <SignOutDialog open={signOut} onOpenChange={setSignOut} />
    </main>
  )
}
