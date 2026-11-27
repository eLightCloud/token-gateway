import { useMutation, useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { PasswordInput } from '@/components/password-input'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { encryptPassword } from '@/features/auth/lib/password-encryption'
import type { TwoFASetupData } from '@/features/security/api'
import { TwoFASetupDialog } from '@/features/security/components/dialogs/two-fa-setup-dialog'
import { api } from '@/lib/api'
import {
  AuthOperationError,
  authRequestOptions,
  authResult,
} from '@/lib/secure-verification'

import {
  rememberAgentActivation,
  type ActivationLink,
} from './lib/agent-activation-location'
import { PasswordSetupForm } from './password-setup-form'

const activationAPI = '/api/security/agent-activation'
const anonymousRequest = { ...authRequestOptions, skipAuthRefresh: true }
type BindingResult = { enabled: boolean; notification_warning?: boolean }

export function AgentActivation(props: {
  link?: ActivationLink
  bindingID?: string
}) {
  const { t } = useTranslation()
  const [bindingID, setBindingID] = useState(props.bindingID)
  const [completed, setCompleted] = useState<BindingResult>()
  const [requestAnotherLink, setRequestAnotherLink] = useState(false)
  const resumed = props.link?.purpose === 'security_agent_resume'
  const setPassword = useMutation({
    gcTime: 0,
    meta: { errorToast: false },
    mutationFn: async (password: string) => {
      const encrypted = await encryptPassword(password)
      return authResult<{ agent_binding_id: string }>(
        api.post(
          resumed
            ? `${activationAPI}/resume/complete`
            : '/api/security/activation',
          { ...props.link, ...encrypted },
          anonymousRequest
        )
      )
    },
    onSuccess: (result) => {
      rememberAgentActivation(result.agent_binding_id)
      setBindingID(result.agent_binding_id)
      setPassword.reset()
    },
  })

  const invalidLink =
    setPassword.error instanceof AuthOperationError &&
    setPassword.error.code === 'AUTH_FLOW_INVALID'

  return (
    <main className='mx-auto flex min-h-svh w-full max-w-lg flex-col justify-center gap-4 p-6'>
      <h1 className='text-2xl font-semibold'>
        {t('Activate your reseller account')}
      </h1>
      {completed && (
        <>
          <p role='status'>
            {completed.enabled
              ? t('Reseller account activated.')
              : t(
                  'Reseller account activated. Waiting for administrator enablement.'
                )}
          </p>
          {completed.enabled && (
            <Link to='/sign-in' search={{ passwordSetupComplete: true }}>
              {t('Sign In')}
            </Link>
          )}
        </>
      )}
      {!completed && bindingID && (
        <AgentActivationBinding
          bindingID={bindingID}
          onComplete={setCompleted}
        />
      )}
      {!completed && !bindingID && !props.link && (
        <AgentActivationResumeRequest />
      )}
      {!completed && !bindingID && props.link && (
        <>
          {setPassword.isError && (
            <p role='alert' className='text-destructive text-sm'>
              {invalidLink && resumed
                ? t('The password or activation link is invalid.')
                : activationError(setPassword.error, t)}
            </p>
          )}
          {resumed ? (
            <form
              className='space-y-4'
              onSubmit={(event) => {
                event.preventDefault()
                const password = new FormData(event.currentTarget).get(
                  'password'
                )
                if (typeof password === 'string' && !setPassword.isPending) {
                  setPassword.mutate(password)
                }
              }}
            >
              <Label htmlFor='activation-password'>{t('Password')}</Label>
              <PasswordInput
                id='activation-password'
                name='password'
                required
                autoComplete='current-password'
                disabled={setPassword.isPending}
              />
              <Button type='submit' disabled={setPassword.isPending}>
                {t('Continue activation')}
              </Button>
            </form>
          ) : (
            <PasswordSetupForm
              pending={setPassword.isPending}
              onConfirm={setPassword.mutate}
            />
          )}
          {invalidLink && !requestAnotherLink && (
            <Button
              variant='outline'
              onClick={() => setRequestAnotherLink(true)}
            >
              {t('Request a continuation link')}
            </Button>
          )}
          {requestAnotherLink && <AgentActivationResumeRequest />}
        </>
      )}
    </main>
  )
}

function AgentActivationBinding(props: {
  bindingID: string
  onComplete: (result: BindingResult) => void
}) {
  const { t } = useTranslation()
  const [dialogOpen, setDialogOpen] = useState(true)
  const bindingRequest = {
    ...anonymousRequest,
    headers: { 'X-Agent-Activation': props.bindingID },
  }
  const setup = useQuery({
    queryKey: ['agent-activation-mfa', props.bindingID],
    queryFn: ({ signal }) =>
      authResult<
        Pick<TwoFASetupData, 'secret' | 'qr_code_data' | 'backup_codes'>
      >(
        api.get(`${activationAPI}/mfa`, {
          ...bindingRequest,
          signal,
          disableDuplicate: true,
        })
      ),
    gcTime: 0,
    retry: false,
    refetchOnMount: 'always',
    refetchOnWindowFocus: false,
    refetchOnReconnect: false,
    meta: { errorToast: false },
  })
  const finish = useMutation({
    gcTime: 0,
    meta: { errorToast: false },
    mutationFn: (code: string) =>
      authResult<BindingResult>(
        api.post(`${activationAPI}/mfa`, { code }, bindingRequest)
      ),
    onSuccess: (data) => {
      props.onComplete(data)
      if (data.notification_warning) {
        toast.warning(
          t(
            'The change succeeded, but the notification email could not be sent.'
          )
        )
      }
    },
  })

  const setupExpired = [setup.error, finish.error].some(
    (error) =>
      error instanceof AuthOperationError && error.code === 'AUTH_FLOW_INVALID'
  )
  if (setupExpired) {
    return <AgentActivationResumeRequest />
  }
  if (setup.isError) {
    return (
      <>
        <p role='alert'>{activationError(setup.error, t)}</p>
        <Button variant='outline' onClick={() => void setup.refetch()}>
          {t('Retry')}
        </Button>
      </>
    )
  }
  return (
    <>
      {setup.isPending && <p role='status'>{t('Setting up 2FA...')}</p>}
      {!dialogOpen && setup.data && (
        <Button onClick={() => setDialogOpen(true)}>
          {t('Setup Two-Factor Authentication')}
        </Button>
      )}
      {dialogOpen && setup.data && (
        <TwoFASetupDialog
          open
          setupData={setup.data}
          loading={finish.isPending}
          initializing={false}
          error={finish.error ? activationError(finish.error, t) : undefined}
          onCancel={() => {
            if (!finish.isPending) setDialogOpen(false)
          }}
          onEnable={async (code) => {
            await finish.mutateAsync(code).catch(() => undefined)
          }}
        />
      )}
    </>
  )
}

function AgentActivationResumeRequest() {
  const { t } = useTranslation()
  const request = useMutation({
    gcTime: 0,
    meta: { errorToast: false },
    mutationFn: (email: string) =>
      authResult(
        api.post(`${activationAPI}/resume`, { email }, anonymousRequest)
      ),
  })

  if (request.isSuccess) {
    return (
      <p role='status'>
        {t('If your account can resume activation, you will receive an email.')}
      </p>
    )
  }
  return (
    <form
      className='space-y-4'
      onSubmit={(event) => {
        event.preventDefault()
        const email = new FormData(event.currentTarget).get('email')
        if (typeof email === 'string' && !request.isPending) {
          request.mutate(email)
        }
      }}
    >
      <p>
        {t(
          'If you have already set your password, request a link to continue activation. If you have not set it, ask your administrator for a new activation link.'
        )}
      </p>
      <Label htmlFor='activation-email'>{t('Email')}</Label>
      <Input
        id='activation-email'
        name='email'
        type='email'
        autoComplete='email'
        required
        disabled={request.isPending}
      />
      {request.isError && (
        <p role='alert'>{activationError(request.error, t)}</p>
      )}
      <Button type='submit' disabled={request.isPending}>
        {t('Request a continuation link')}
      </Button>
    </form>
  )
}

function activationError(
  error: unknown,
  t: ReturnType<typeof useTranslation>['t']
) {
  if (error instanceof AuthOperationError) {
    if (error.code === 'AUTH_FLOW_INVALID') {
      return t('This link is invalid or has expired. Request a new link.')
    }
    return t(error.message)
  }
  return t('Please try again later.')
}
