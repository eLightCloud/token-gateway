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
import { useEffect, useState } from 'react'
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

import { AgentActivation } from './agent-activation'
import { confirmSecurityEmail } from './api'
import {
  rememberAgentActivation,
  type ActivationLink,
} from './lib/agent-activation-location'
import { PasswordSetupForm } from './password-setup-form'

function readActivationLink() {
  const params = new URLSearchParams(window.location.hash.slice(1))
  const search = new URLSearchParams(window.location.search)
  const token = params.get('token') ?? ''
  const bindingID = search.get('binding')
  return {
    link: {
      token,
      user_id: Number(params.get('user_id')),
      purpose: params.get('purpose'),
    },
    agent:
      params.get('account') === 'agent' ||
      params.get('purpose') === 'security_agent_resume' ||
      (!token && search.get('reseller') === '1'),
    bindingID:
      !token && bindingID && /^[1-9][0-9]{0,18}$/.test(bindingID)
        ? bindingID
        : undefined,
  }
}

export function AccountActivation() {
  const [{ link, agent, bindingID, revision }, setActivation] = useState(
    () => ({
      ...readActivationLink(),
      revision: 0,
    })
  )
  useEffect(() => {
    const clearLink = (incoming: ReturnType<typeof readActivationLink>) => {
      if (incoming.agent) {
        rememberAgentActivation()
      } else {
        window.history.replaceState(
          window.history.state,
          '',
          window.location.pathname
        )
      }
    }
    if (window.location.hash) clearLink(readActivationLink())

    const handleLink = () => {
      if (!window.location.hash) return
      const next = readActivationLink()
      clearLink(next)
      setActivation((current) => ({ ...next, revision: current.revision + 1 }))
    }
    window.addEventListener('hashchange', handleLink)
    return () => window.removeEventListener('hashchange', handleLink)
  }, [])

  if (agent) {
    return (
      <AgentActivation
        key={revision}
        link={link.token ? link : undefined}
        bindingID={bindingID}
      />
    )
  }
  return <SecurityLinkActivation key={revision} link={link} />
}

function SecurityLinkActivation({ link }: { link: ActivationLink }) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const confirmsEmail =
    link.purpose === 'security_email' ||
    link.purpose === 'security_email_change'
  const resetsPassword = link.purpose === 'security_password_reset'
  const complete = useMutation<
    AccountSecurityResult & { agent_binding_id?: string },
    Error,
    string
  >({
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
      return authResult<AccountSecurityResult & { agent_binding_id?: string }>(
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
      if (result?.agent_binding_id) {
        rememberAgentActivation(result.agent_binding_id)
        return
      }

      toast.success(
        resetsPassword
          ? t('Password reset completed. Sign in with your new password.')
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
  if (complete.data?.agent_binding_id) {
    return <AgentActivation bindingID={complete.data.agent_binding_id} />
  }
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
      {complete.error instanceof AuthOperationError &&
        complete.error.code === 'AUTH_FLOW_INVALID' &&
        link.purpose === 'account_activation' && (
          <a href='/account-activation?reseller=1'>
            {t('Resume reseller activation')}
          </a>
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
