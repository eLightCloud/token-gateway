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
import { useState } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { CopyButton } from '@/components/copy-button'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { PasswordInput } from '@/components/password-input'
import { Button } from '@/components/ui/button'
import {
  Form,
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'
import { handleServerError } from '@/lib/handle-server-error'

import {
  SettingsForm,
  SettingsSwitchContent,
  SettingsSwitchItem,
} from '../components/settings-form-layout'
import { SettingsPageFormActions } from '../components/settings-page-context'
import { SettingsSection } from '../components/settings-section'
import {
  getVantageIntegrationSettings,
  saveVantageIntegrationSettings,
  vantageIntegrationQueryKey,
  type VantageIntegrationSettings,
  type VantageIntegrationUpdate,
} from './api'
import { createVantageIntegrationSchema, parseVantageIPv4List } from './schema'

export function VantageIntegrationSettingsSection() {
  const { t } = useTranslation()
  const settings = useQuery({
    queryKey: vantageIntegrationQueryKey,
    queryFn: getVantageIntegrationSettings,
    refetchOnWindowFocus: false,
  })
  if (settings.isPending) {
    return <LoadingState message={t('Loading settings...')} />
  }
  if (!settings.data) {
    return (
      <ErrorState
        title={t('Failed to load settings')}
        onRetry={() => void settings.refetch()}
      />
    )
  }
  return <VantageIntegrationForm settings={settings.data} />
}

function VantageIntegrationForm(props: {
  settings: VantageIntegrationSettings
}) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [pending, setPending] = useState<VantageIntegrationUpdate | null>(null)
  const form = useForm({
    resolver: zodResolver(
      createVantageIntegrationSchema(t, props.settings.secret_configured)
    ),
    defaultValues: {
      enabled: props.settings.enabled,
      allowed_ipv4: props.settings.allowed_ipv4.join('\n'),
      secret: '',
    },
  })
  const save = useMutation({
    mutationFn: saveVantageIntegrationSettings,
    gcTime: 0,
    meta: { errorToast: false },
    onSuccess: (settings) => {
      queryClient.setQueryData(vantageIntegrationQueryKey, settings)
      form.reset({
        enabled: settings.enabled,
        allowed_ipv4: settings.allowed_ipv4.join('\n'),
        secret: '',
      })
      toast.success(t('Vantage integration settings saved'))
    },
    onError: (error) =>
      handleServerError(
        error,
        t('Failed to save Vantage integration settings')
      ),
  })
  const submit = form.handleSubmit((values) =>
    setPending({
      enabled: values.enabled,
      allowed_ipv4: parseVantageIPv4List(values.allowed_ipv4),
      secret: values.secret,
    })
  )
  const draftSecret = form.watch('secret')

  return (
    <SettingsSection title={t('Vantage Integration')}>
      <Form {...form}>
        <SettingsForm onSubmit={submit} autoComplete='off'>
          <SettingsPageFormActions
            onSave={submit}
            isSaving={save.isPending}
            isSaveDisabled={!form.formState.isDirty}
          />
          <p className='text-muted-foreground text-sm'>
            {t(
              'Saved changes apply to new requests on all nodes without restarting.'
            )}
          </p>
          <FormField
            control={form.control}
            name='enabled'
            render={({ field }) => (
              <SettingsSwitchItem>
                <SettingsSwitchContent>
                  <FormLabel>{t('Enable Vantage integration')}</FormLabel>
                  <FormDescription>
                    {t(
                      'Require the application secret and an allowed IPv4 source for every integration request.'
                    )}
                  </FormDescription>
                </SettingsSwitchContent>
                <FormControl>
                  <Switch
                    checked={field.value}
                    onCheckedChange={field.onChange}
                    disabled={save.isPending}
                  />
                </FormControl>
              </SettingsSwitchItem>
            )}
          />
          <FormField
            control={form.control}
            name='secret'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Application Secret')}</FormLabel>
                <FormControl>
                  <PasswordInput
                    {...field}
                    disabled={save.isPending}
                    autoComplete='new-password'
                    placeholder={t('Leave blank to keep the existing secret')}
                  />
                </FormControl>
                <FormDescription>
                  {props.settings.secret_configured
                    ? t('An application secret is configured.')
                    : t('No application secret is configured.')}{' '}
                  {t(
                    'Save the new secret in the Vantage backend. Saved secrets cannot be displayed again.'
                  )}
                </FormDescription>
                <div className='flex flex-wrap items-center gap-2'>
                  <Button
                    type='button'
                    variant='outline'
                    size='sm'
                    disabled={save.isPending}
                    onClick={() => {
                      const bytes = crypto.getRandomValues(new Uint8Array(32))
                      form.setValue(
                        'secret',
                        Array.from(bytes, (byte) =>
                          byte.toString(16).padStart(2, '0')
                        ).join(''),
                        { shouldDirty: true, shouldValidate: true }
                      )
                    }}
                  >
                    {t('Generate application secret')}
                  </Button>
                  {draftSecret && (
                    <CopyButton
                      value={draftSecret}
                      size='sm'
                      variant='outline'
                      tooltip={t('Copy application secret')}
                    >
                      {t('Copy')}
                    </CopyButton>
                  )}
                </div>
                <FormMessage />
              </FormItem>
            )}
          />
          <FormField
            control={form.control}
            name='allowed_ipv4'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('IPv4 Source Allowlist')}</FormLabel>
                <FormControl>
                  <Textarea
                    {...field}
                    disabled={save.isPending}
                    rows={5}
                    spellCheck={false}
                    placeholder={'10.0.0.8\n10.0.0.9'}
                  />
                </FormControl>
                <FormDescription>
                  {t(
                    'Enter complete IPv4 addresses separated by commas or new lines. IPv6, CIDR ranges and hostnames are not supported.'
                  )}
                </FormDescription>
                <FormDescription>
                  {t(
                    'The allowlist checks the TCP peer address. Forwarded headers are ignored.'
                  )}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />
        </SettingsForm>
      </Form>
      <ConfirmDialog
        open={pending !== null}
        onOpenChange={(open) => {
          if (!open && !save.isPending) setPending(null)
        }}
        title={t('Save Vantage integration settings?')}
        desc={t(
          'Changes affect new integration requests immediately. If changing the secret, update the Vantage backend to match.'
        )}
        confirmText={t('Save Changes')}
        isLoading={save.isPending}
        handleConfirm={() => {
          if (!pending) return
          save.mutate(pending, {
            onSettled: () => {
              setPending(null)
              save.reset()
            },
          })
        }}
      />
    </SettingsSection>
  )
}
