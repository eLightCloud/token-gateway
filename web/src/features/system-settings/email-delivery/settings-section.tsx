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
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useRef, useState } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Button } from '@/components/ui/button'
import {
  Form,
  FormControl,
  FormField,
  FormItem,
  FormLabel,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import {
  SecureVerificationDialog,
  useSecureVerification,
} from '@/features/auth/secure-verification'
import { handleServerError } from '@/lib/handle-server-error'
import { getServerErrorSources } from '@/lib/server-error-message'

import { FormNavigationGuard } from '../components/form-navigation-guard'
import { SettingsForm } from '../components/settings-form-layout'
import {
  SettingsPageFormActions,
  useSuppressSettingsSectionHeader,
} from '../components/settings-page-context'
import { SettingsSection } from '../components/settings-section'
import {
  emailSettingsQueryKey,
  getEmailSettings,
  saveEmailSettings,
  testEmailSettings,
} from './api'
import { EmailProviderFields } from './provider-fields'
import { createEmailSchema, emailFormDefaults } from './schema'
import { EmailTestResult } from './test-result'
import type { EmailReport, EmailSettings, EmailUpdate } from './types'

export function EmailDeliverySettingsSection() {
  const { t } = useTranslation()
  const query = useQuery({
    queryKey: emailSettingsQueryKey,
    queryFn: getEmailSettings,
    refetchOnWindowFocus: false,
    meta: { errorToast: false },
  })
  if (query.isPending) {
    return <LoadingState message={t('Loading settings...')} />
  }
  const encryptionUnavailable = getServerErrorSources(query.error).some(
    (source) => source.code === 'EMAIL_ENCRYPTION_UNAVAILABLE'
  )
  if (!query.data || encryptionUnavailable) {
    return (
      <ErrorState
        title={t('Failed to load settings')}
        description={
          encryptionUnavailable
            ? t(
                'Email credentials cannot be decrypted. Restore the original EMAIL_DELIVERY_STORAGE_KEY on every server and retry.'
              )
            : undefined
        }
        onRetry={() => void query.refetch()}
      />
    )
  }
  return <EmailDeliveryForm settings={query.data} />
}

function EmailDeliveryForm(props: { settings: EmailSettings }) {
  const { t } = useTranslation()
  const client = useQueryClient()
  const pageOwnsActions = useSuppressSettingsSectionHeader()
  const verification = useSecureVerification()
  const [baseline, setBaseline] = useState(props.settings)
  const configurationChanged = props.settings.revision !== baseline.revision
  const [busy, setBusy] = useState(false)
  const operationPending = useRef(false)
  const [message, setMessage] = useState('')
  const [recipient, setRecipient] = useState('')
  const [report, setReport] = useState<EmailReport | null>(null)
  const [clear, setClear] = useState<
    'client_secret' | 'legacy_password' | null
  >(null)
  const errorRef = useRef<HTMLDivElement>(null)
  const form = useForm<EmailUpdate>({
    resolver: zodResolver(createEmailSchema(t, baseline)),
    defaultValues: emailFormDefaults(baseline),
  })
  const microsoft =
    form.watch('intent.options.auth_mode') === 'microsoft_oauth2_app'
  const save = useMutation({
    mutationFn: async (update: EmailUpdate) => {
      const proof = await verification.requestVerification({
        scope: 'email.delivery.settings.write',
        context: update.intent,
      })
      if (!proof) return null
      return saveEmailSettings(update, proof.proof_token)
    },
    gcTime: 0,
    meta: { errorToast: false },
  })
  const onSave = form.handleSubmit(
    async (values) => {
      if (operationPending.current || configurationChanged) return
      operationPending.current = true
      setBusy(true)
      setMessage('')
      try {
        const settings = await save.mutateAsync(structuredClone(values))
        if (settings) {
          client.setQueryData(emailSettingsQueryKey, settings)
          setBaseline(settings)
          form.reset(emailFormDefaults(settings))
          setMessage(
            t(
              'Email settings saved. Sending has not been verified for this version.'
            )
          )
        }
      } catch (error) {
        handleServerError(error)
        setMessage(
          t(
            'Unable to save email settings. Check the fields; if settings changed elsewhere, reload and compare before saving.'
          )
        )
      } finally {
        operationPending.current = false
        setBusy(false)
        save.reset()
      }
    },
    () => {
      setMessage(t('Check the highlighted email configuration fields.'))
      queueMicrotask(() => errorRef.current?.focus())
    }
  )
  const onTest = async () => {
    if (
      operationPending.current ||
      configurationChanged ||
      form.formState.isDirty
    ) {
      return
    }
    if (!/^[^\s@;,]+@[^\s@;,]+\.[^\s@;,]+$/.test(recipient)) {
      setMessage(t('Enter a valid test recipient email.'))
      return
    }
    operationPending.current = true
    setBusy(true)
    setMessage('')
    const intent = {
      expected_revision: baseline.revision,
      recipient: recipient.trim(),
    }
    try {
      const proof = await verification.requestVerification({
        scope: 'email.delivery.test.send',
        context: intent,
      })
      if (proof) setReport(await testEmailSettings(intent, proof.proof_token))
    } catch (error) {
      handleServerError(error)
      setMessage(
        t(
          'Unable to run the email test. Check permissions, configuration version and rate limits.'
        )
      )
    } finally {
      operationPending.current = false
      setBusy(false)
    }
  }
  return (
    <SettingsSection title={t('Email')}>
      <FormNavigationGuard when={form.formState.isDirty && !busy} />
      {configurationChanged && (
        <p role='status' className='text-sm'>
          {t(
            'Email settings changed elsewhere. Reload saved settings before saving or testing.'
          )}
        </p>
      )}
      {!baseline.storage_key_ready && (
        <p role='status' className='text-sm'>
          {t(
            'Configure EMAIL_DELIVERY_STORAGE_KEY on every server before saving credentials'
          )}
        </p>
      )}
      <Form {...form}>
        <SettingsForm onSubmit={onSave} autoComplete='off'>
          <SettingsPageFormActions
            onSave={() => void onSave()}
            isSaving={busy}
            isSaveDisabled={configurationChanged}
            saveLabel='Save email settings'
          />
          <fieldset
            disabled={busy}
            className='grid min-w-0 gap-6 md:grid-cols-2'
          >
            <FormField
              control={form.control}
              name='intent.options.auth_mode'
              render={({ field }) => (
                <FormItem className='md:col-span-2'>
                  <FormLabel htmlFor='email-auth-mode'>
                    {t('Authentication method')}
                  </FormLabel>
                  <FormControl>
                    <NativeSelect {...field} id='email-auth-mode'>
                      <NativeSelectOption value='legacy'>
                        {t('Existing SMTP')}
                      </NativeSelectOption>
                      <NativeSelectOption value='microsoft_oauth2_app'>
                        {t('Microsoft 365 (Microsoft Graph)')}
                      </NativeSelectOption>
                    </NativeSelect>
                  </FormControl>
                </FormItem>
              )}
            />
            <EmailProviderFields
              form={form}
              settings={baseline}
              microsoft={microsoft}
              onClear={setClear}
            />
          </fieldset>
          <div
            ref={errorRef}
            tabIndex={-1}
            role='status'
            aria-live='polite'
            className='space-y-1 text-sm'
          >
            {message && <p>{message}</p>}
            {busy && (
              <p>{t('Waiting for verification or email operation...')}</p>
            )}
          </div>
          <div className='flex flex-wrap gap-2'>
            {!pageOwnsActions && (
              <Button type='submit' disabled={busy || configurationChanged}>
                {t('Save email settings')}
              </Button>
            )}
            <Button
              type='button'
              variant='outline'
              disabled={busy}
              onClick={async () => {
                try {
                  const settings = await client.fetchQuery({
                    queryKey: emailSettingsQueryKey,
                    queryFn: getEmailSettings,
                    staleTime: 0,
                  })
                  setBaseline(settings)
                  form.reset(emailFormDefaults(settings))
                  setMessage('')
                } catch (error) {
                  handleServerError(error)
                }
              }}
            >
              {t('Reload saved email settings')}
            </Button>
          </div>
        </SettingsForm>
      </Form>
      <div className='mt-6 space-y-3 border-t pt-6'>
        <label htmlFor='email-test-recipient' className='text-sm font-medium'>
          {t('Test recipient')}
        </label>
        <Input
          id='email-test-recipient'
          type='email'
          autoComplete='off'
          value={recipient}
          disabled={busy}
          onChange={(event) => setRecipient(event.target.value)}
        />
        <p className='text-muted-foreground text-sm'>
          {t(
            'This sends one real email using saved settings. Save your changes before testing.'
          )}
        </p>
        <Button
          type='button'
          variant='outline'
          disabled={
            busy || configurationChanged || form.formState.isDirty || !recipient
          }
          onClick={() => void onTest()}
        >
          {t('Send test email')}
        </Button>
        {report && (
          <EmailTestResult report={report} revision={props.settings.revision} />
        )}
      </div>
      <ConfirmDialog
        open={clear !== null}
        onOpenChange={(open) => {
          if (!open) setClear(null)
        }}
        title={t('Clear saved credential?')}
        desc={t(
          'The credential will be removed only when you save. Switch away from Microsoft before clearing its active secret.'
        )}
        confirmText={t('Clear')}
        destructive
        handleConfirm={() => {
          if (!clear) return
          form.setValue(clear, '', { shouldDirty: true })
          form.setValue(
            clear === 'client_secret'
              ? 'intent.client_secret_action'
              : 'intent.legacy_password_action',
            'clear',
            { shouldDirty: true }
          )
          setClear(null)
        }}
      />
      <SecureVerificationDialog {...verification.dialogProps} />
    </SettingsSection>
  )
}
