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
import { useQueryClient } from '@tanstack/react-query'
import { useMemo } from 'react'
import { useForm, useWatch } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import { PasswordInput } from '@/components/password-input'
import {
  Form,
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { RadioGroup, RadioGroupItem } from '@/components/ui/radio-group'
import { Switch } from '@/components/ui/switch'
import {
  SecureVerificationDialog,
  useSecureVerification,
} from '@/features/auth/secure-verification'
import { getCurrencyDisplay, getCurrencyLabel } from '@/lib/currency'
import { parseQuotaFromDollars, quotaUnitsToDollars } from '@/lib/format'
import { handleServerError } from '@/lib/handle-server-error'

import {
  SettingsControlChildren,
  SettingsControlGroup,
  SettingsForm,
  SettingsSwitchContent,
  SettingsSwitchItem,
} from '../components/settings-form-layout'
import {
  SettingsPageFormActions,
  SettingsPageProvider,
} from '../components/settings-page-context'
import { SettingsSection } from '../components/settings-section'
import { saveResendSettings } from '../email-delivery/api'
import { EmailDeliverySettingsSection } from '../email-delivery/settings-section'
import { useResetForm } from '../hooks/use-reset-form'
import { useUpdateOption } from '../hooks/use-update-option'
import { OperationsSettingsSection } from '../operations/settings-section'
import {
  createEmailSchema,
  parseResendSender,
  type EmailFormValues,
} from './email-settings-schema'

type EmailSettingsSectionProps = {
  defaultValues: Omit<EmailFormValues, 'ResendSenderName'>
}

export function EmailSettingsSection({
  defaultValues,
}: EmailSettingsSectionProps) {
  const { t } = useTranslation()
  const updateOption = useUpdateOption()
  const queryClient = useQueryClient()
  const verification = useSecureVerification()
  const emailSchema = createEmailSchema(t)
  const initialValues = useMemo(
    () => ({
      ...defaultValues,
      ...parseResendSender(defaultValues.ResendFrom),
      QuotaRemindThreshold: String(
        quotaUnitsToDollars(Number(defaultValues.QuotaRemindThreshold) || 0)
      ),
    }),
    [defaultValues]
  )

  const form = useForm<EmailFormValues>({
    resolver: zodResolver(emailSchema),
    defaultValues: initialValues,
  })

  const [provider, quotaRemindEnabled] = useWatch({
    control: form.control,
    name: ['EmailProvider', 'QuotaRemindEnabled'],
  })
  const tokensOnly = getCurrencyDisplay().meta.kind === 'tokens'
  const isSaving = form.formState.isSubmitting || updateOption.isPending
  useResetForm(form, isSaving ? undefined : initialValues)

  const onSubmit = async (values: EmailFormValues) => {
    const updates: Array<{ key: string; value: string | boolean }> = []
    const from = values.ResendFrom.trim()
    const name = values.ResendSenderName.trim()
    const key =
      values.EmailProvider === 'resend' ? values.ResendApiKey.trim() : ''
    const changed =
      values.EmailProvider !== initialValues.EmailProvider ||
      (values.EmailProvider === 'resend' &&
        (key.length > 0 ||
          from !== initialValues.ResendFrom ||
          name !== initialValues.ResendSenderName))

    if (values.QuotaRemindEnabled !== defaultValues.QuotaRemindEnabled) {
      updates.push({
        key: 'QuotaRemindEnabled',
        value: values.QuotaRemindEnabled,
      })
    }

    const threshold = values.QuotaRemindThreshold.trim()
    if (threshold !== initialValues.QuotaRemindThreshold) {
      updates.push({
        key: 'QuotaRemindThreshold',
        value: String(parseQuotaFromDollars(Number(threshold || 0))),
      })
    }

    try {
      if (changed) {
        const escapedName = name.replaceAll('\\', '\\\\').replaceAll('"', '\\"')
        const sender = name ? `"${escapedName}" <${from}>` : from
        const intent = {
          provider: values.EmailProvider,
          from: values.EmailProvider === 'resend' ? sender : '',
          replace_key: Boolean(key),
        }
        const proof = await verification.requestVerification({
          scope: 'email.delivery.resend.write',
          context: intent,
        })
        if (!proof) return
        await saveResendSettings(intent, key, proof.proof_token)
        if (key) form.setValue('ResendKeyConfigured', true)
        form.setValue('ResendApiKey', '')
        void queryClient.invalidateQueries({ queryKey: ['system-options'] })
      }
      for (const update of updates) {
        await updateOption.mutateAsync(update)
      }
    } catch (error) {
      handleServerError(error)
    }
  }

  return (
    <div className='space-y-8'>
      <SettingsSection title={t('Email & Notifications')}>
        <Form {...form}>
          <SettingsForm
            onSubmit={form.handleSubmit(onSubmit)}
            autoComplete='off'
          >
            <SettingsPageFormActions
              onSave={form.handleSubmit(onSubmit)}
              isSaving={isSaving}
            />
            <FormField
              control={form.control}
              name='EmailProvider'
              render={({ field }) => (
                <FormItem data-settings-form-span='full'>
                  <FormLabel>{t('Email sending method')}</FormLabel>
                  <FormControl>
                    <RadioGroup
                      value={field.value}
                      onValueChange={field.onChange}
                      disabled={isSaving}
                      className='grid-cols-1 gap-3 sm:grid-cols-2'
                    >
                      <Label
                        className='flex cursor-pointer items-center gap-3 rounded-lg border p-4'
                        htmlFor='email-provider-smtp'
                      >
                        <RadioGroupItem value='smtp' id='email-provider-smtp' />
                        SMTP / {t('Microsoft 365 (Microsoft Graph)')}
                      </Label>
                      <Label
                        className='flex cursor-pointer items-center gap-3 rounded-lg border p-4'
                        htmlFor='email-provider-resend'
                      >
                        <RadioGroupItem
                          value='resend'
                          id='email-provider-resend'
                        />
                        Resend API
                      </Label>
                    </RadioGroup>
                  </FormControl>
                  <FormDescription>
                    {t(
                      'Account and system email uses the selected provider. Other provider settings are kept.'
                    )}
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />
            {provider === 'resend' && (
              <>
                <FormField
                  control={form.control}
                  name='ResendFrom'
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>{t('Sender email address')}</FormLabel>
                      <FormControl>
                        <Input
                          {...field}
                          autoComplete='off'
                          disabled={isSaving}
                          type='email'
                          placeholder='noreply@example.com'
                        />
                      </FormControl>
                      <FormDescription>
                        {t('The domain must be verified in Resend')}
                      </FormDescription>
                      <FormMessage />
                    </FormItem>
                  )}
                />
                <FormField
                  control={form.control}
                  name='ResendSenderName'
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>{t('Sender name')}</FormLabel>
                      <FormControl>
                        <Input
                          {...field}
                          autoComplete='off'
                          disabled={isSaving}
                          placeholder='New API'
                        />
                      </FormControl>
                      <FormDescription>
                        {t(
                          'The name recipients see. Leave blank to use the site name.'
                        )}
                      </FormDescription>
                      <FormMessage />
                    </FormItem>
                  )}
                />
                <FormField
                  control={form.control}
                  name='ResendApiKey'
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>{t('Resend API key')}</FormLabel>
                      <FormControl>
                        <PasswordInput
                          {...field}
                          autoComplete='new-password'
                          disabled={isSaving}
                          placeholder={t('Enter new token to update')}
                        />
                      </FormControl>
                      <FormDescription>
                        {t('Leave blank to keep the existing key')}
                      </FormDescription>
                      <FormMessage />
                    </FormItem>
                  )}
                />
              </>
            )}

            <SettingsControlGroup>
              <FormField
                control={form.control}
                name='QuotaRemindEnabled'
                render={({ field }) => (
                  <SettingsSwitchItem>
                    <SettingsSwitchContent>
                      <FormLabel>{t('Low balance reminder')}</FormLabel>
                      <FormDescription>
                        {t(
                          'Remind users through their chosen notification method when their balance drops below the threshold'
                        )}
                      </FormDescription>
                    </SettingsSwitchContent>
                    <FormControl>
                      <Switch
                        checked={field.value}
                        onCheckedChange={field.onChange}
                      />
                    </FormControl>
                  </SettingsSwitchItem>
                )}
              />

              {quotaRemindEnabled && (
                <SettingsControlChildren>
                  <FormField
                    control={form.control}
                    name='QuotaRemindThreshold'
                    render={({ field }) => (
                      <FormItem>
                        <FormLabel>
                          {t('Default reminder threshold')} (
                          {getCurrencyLabel()})
                        </FormLabel>
                        <FormControl>
                          <Input
                            autoComplete='off'
                            type='number'
                            min={0}
                            step={tokensOnly ? 1 : 0.000001}
                            {...field}
                            onChange={(event) =>
                              field.onChange(event.target.value)
                            }
                          />
                        </FormControl>
                        <FormMessage />
                      </FormItem>
                    )}
                  />
                </SettingsControlChildren>
              )}
            </SettingsControlGroup>
          </SettingsForm>
        </Form>
      </SettingsSection>
      <div hidden={provider === 'resend'}>
        <SettingsPageProvider
          actionsContainer={null}
          suppressSectionHeader={false}
        >
          <EmailDeliverySettingsSection />
        </SettingsPageProvider>
      </div>
      <OperationsSettingsSection />
      <SecureVerificationDialog {...verification.dialogProps} />
    </div>
  )
}
