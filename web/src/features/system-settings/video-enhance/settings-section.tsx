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
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
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
import { Input } from '@/components/ui/input'
import { Switch } from '@/components/ui/switch'
import {
  SecureVerificationDialog,
  useSecureVerification,
} from '@/features/auth/secure-verification'
import { handleServerError } from '@/lib/handle-server-error'

import { SettingsForm } from '../components/settings-form-layout'
import { SettingsPageFormActions } from '../components/settings-page-context'
import { SettingsSection } from '../components/settings-section'
import {
  getVideoEnhanceSettings,
  saveVideoEnhanceSettings,
  testVideoEnhanceStorage,
  videoEnhanceSettingsKey,
  type VideoEnhanceSettings,
  type VideoEnhanceUpdate,
} from './api'
import { videoEnhanceSettingsSchema } from './schema'

export function VideoEnhanceSettingsSection() {
  const { t } = useTranslation()
  const settings = useQuery({
    queryKey: videoEnhanceSettingsKey,
    queryFn: getVideoEnhanceSettings,
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
  return <VideoEnhanceForm settings={settings.data} />
}

function VideoEnhanceForm(props: { settings: VideoEnhanceSettings }) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const verification = useSecureVerification()
  const [pending, setPending] = useState(false)
  const [testStatus, setTestStatus] = useState('')
  const form = useForm<VideoEnhanceUpdate>({
    resolver: zodResolver(videoEnhanceSettingsSchema),
    defaultValues: {
      ...props.settings,
      mediakit_key: '',
      access_key_id: '',
      access_key_secret: '',
    },
  })
  const fields = [
    { name: 'mediakit_key', label: 'MediaKit API key', secret: true },
    { name: 'queue_id', label: 'MediaKit queue ID', secret: false },
    { name: 'region', label: 'OSS region', secret: false },
    { name: 'endpoint', label: 'OSS public endpoint', secret: false },
    { name: 'bucket', label: 'OSS bucket', secret: false },
    { name: 'prefix', label: 'OSS source video prefix', secret: false },
    {
      name: 'enhanced_prefix',
      label: 'OSS enhanced video prefix',
      secret: false,
    },
    { name: 'access_key_id', label: 'OSS access key ID', secret: true },
    { name: 'access_key_secret', label: 'OSS access key secret', secret: true },
  ] as const

  async function save(values: VideoEnhanceUpdate) {
    setPending(true)
    try {
      const proof = await verification.requestVerification({
        scope: 'video.enhance.settings.write',
      })
      if (!proof) return
      const saved = await saveVideoEnhanceSettings(values, proof.proof_token)
      queryClient.setQueryData(videoEnhanceSettingsKey, saved)
      form.reset({
        ...saved,
        mediakit_key: '',
        access_key_id: '',
        access_key_secret: '',
      })
      toast.success(t('Settings saved successfully'))
    } catch (error) {
      handleServerError(error)
    } finally {
      setPending(false)
    }
  }

  async function testStorage() {
    setPending(true)
    setTestStatus('')
    try {
      const proof = await verification.requestVerification({
        scope: 'video.enhance.settings.write',
      })
      if (!proof) return
      await testVideoEnhanceStorage(proof.proof_token)
      setTestStatus(
        t(
          'OSS write, read and delete succeeded. Video enhancement was not submitted.'
        )
      )
    } catch (error) {
      handleServerError(error)
    } finally {
      setPending(false)
    }
  }

  return (
    <SettingsSection title={t('Video enhancement and OSS')}>
      <p className='text-muted-foreground text-sm'>
        {t(
          'Seedance enhancement uses MediaKit large-model enhancement and private Alibaba Cloud OSS storage. Blank credentials keep saved values.'
        )}
      </p>
      {!props.settings.encryption_configured && (
        <p role='status' className='text-sm'>
          {t(
            'Set a stable CRYPTO_SECRET or SESSION_SECRET of at least 32 bytes on the server before saving video credentials.'
          )}
        </p>
      )}
      <p role='status' className='text-muted-foreground text-sm'>
        {t('MediaKit')}:{' '}
        {t(
          props.settings.mediakit_configured ? 'Configured' : 'Not configured'
        )}{' '}
        · OSS:{' '}
        {t(props.settings.oss_configured ? 'Configured' : 'Not configured')}
      </p>
      <Form {...form}>
        <SettingsForm onSubmit={form.handleSubmit(save)}>
          <FormField
            control={form.control}
            name='store_generated_videos'
            render={({ field }) => (
              <FormItem className='flex items-center justify-between gap-4'>
                <FormLabel>
                  {t('Store all Seedance generated videos in OSS')}
                </FormLabel>
                <FormControl>
                  <Switch
                    checked={field.value}
                    onCheckedChange={field.onChange}
                    disabled={pending}
                  />
                </FormControl>
              </FormItem>
            )}
          />
          <FormField
            control={form.control}
            name='enabled'
            render={({ field }) => (
              <FormItem className='flex items-center justify-between gap-4'>
                <FormLabel>{t('Enable video enhancement service')}</FormLabel>
                <FormControl>
                  <Switch
                    checked={field.value}
                    onCheckedChange={field.onChange}
                    disabled={pending}
                  />
                </FormControl>
              </FormItem>
            )}
          />
          {fields.map((item) => (
            <FormField
              key={item.name}
              control={form.control}
              name={item.name}
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t(item.label)}</FormLabel>
                  <FormControl>
                    {item.secret ? (
                      <PasswordInput
                        {...field}
                        autoComplete='new-password'
                        disabled={pending}
                      />
                    ) : (
                      <Input {...field} disabled={pending} />
                    )}
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )}
            />
          ))}
          <Button
            type='button'
            variant='outline'
            disabled={
              pending ||
              !props.settings.oss_configured ||
              form.formState.isDirty
            }
            onClick={() => void testStorage()}
          >
            {t('Test saved OSS configuration')}
          </Button>
          {testStatus && (
            <p role='status' className='text-sm'>
              {testStatus}
            </p>
          )}
          <SettingsPageFormActions
            onSave={() => void form.handleSubmit(save)()}
            onReset={() => form.reset()}
            isSaving={pending}
            isSaveDisabled={
              !form.formState.isDirty || !props.settings.encryption_configured
            }
          />
        </SettingsForm>
      </Form>
      <SecureVerificationDialog {...verification.dialogProps} />
    </SettingsSection>
  )
}
