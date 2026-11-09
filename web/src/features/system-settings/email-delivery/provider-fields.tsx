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
import type { UseFormReturn } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import { PasswordInput } from '@/components/password-input'
import { Button } from '@/components/ui/button'
import {
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'

import { SettingsSwitchField } from '../components/settings-form-layout'
import type { EmailSettings, EmailUpdate } from './types'

export function EmailProviderFields(props: {
  form: UseFormReturn<EmailUpdate>
  settings: EmailSettings
  microsoft: boolean
  onClear: (field: 'client_secret' | 'legacy_password') => void
}) {
  const { t } = useTranslation()
  const texts = props.microsoft
    ? ([
        { name: 'intent.options.microsoft.tenant_id', label: t('Tenant ID') },
        {
          name: 'intent.options.microsoft.client_id',
          label: t('Application ID'),
        },
        {
          name: 'intent.options.microsoft.mailbox',
          label: t('Sender mailbox'),
        },
      ] as const)
    : ([
        { name: 'intent.options.legacy.host', label: t('SMTP Host') },
        { name: 'intent.options.legacy.account', label: t('SMTP Account') },
        { name: 'intent.options.legacy.from', label: t('Sender Email') },
      ] as const)
  const secret = props.microsoft ? 'client_secret' : 'legacy_password'
  const action = props.microsoft
    ? 'intent.client_secret_action'
    : 'intent.legacy_password_action'
  const configured = props.microsoft
    ? props.settings.client_secret_configured
    : props.settings.legacy_password_configured
  const cleared = props.form.watch(action) === 'clear'
  let securityMode = 'none'
  if (props.form.watch('intent.options.legacy.ssl')) securityMode = 'ssl'
  else if (props.form.watch('intent.options.legacy.starttls')) {
    securityMode = 'starttls'
  }
  return (
    <>
      {props.microsoft && (
        <p className='text-muted-foreground text-sm md:col-span-2'>
          {t(
            'Sends through Microsoft Graph. The application needs Mail.Send permission for the sender mailbox.'
          )}
        </p>
      )}
      {texts.map((item) => (
        <FormField
          key={item.name}
          control={props.form.control}
          name={item.name}
          render={({ field }) => (
            <FormItem>
              <FormLabel>{item.label}</FormLabel>
              <FormControl>
                <Input {...field} autoComplete='off' spellCheck={false} />
              </FormControl>
              <FormMessage />
            </FormItem>
          )}
        />
      ))}
      {!props.microsoft && (
        <>
          <FormField
            control={props.form.control}
            name='intent.options.legacy.port'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Port')}</FormLabel>
                <FormControl>
                  <Input
                    {...field}
                    type='number'
                    min={1}
                    max={65535}
                    onChange={(event) =>
                      field.onChange(event.target.valueAsNumber)
                    }
                  />
                </FormControl>
                <FormMessage />
              </FormItem>
            )}
          />
          <FormItem>
            <FormLabel htmlFor='email-security'>
              {t('Connection security')}
            </FormLabel>
            <NativeSelect
              id='email-security'
              value={securityMode}
              onChange={(event) => {
                props.form.setValue(
                  'intent.options.legacy.ssl',
                  event.target.value === 'ssl',
                  { shouldDirty: true }
                )
                props.form.setValue(
                  'intent.options.legacy.starttls',
                  event.target.value === 'starttls',
                  { shouldDirty: true }
                )
              }}
            >
              <NativeSelectOption value='none'>{t('None')}</NativeSelectOption>
              <NativeSelectOption value='starttls'>STARTTLS</NativeSelectOption>
              <NativeSelectOption value='ssl'>SSL/TLS</NativeSelectOption>
            </NativeSelect>
          </FormItem>
          <SettingsSwitchField
            label={t('Skip certificate verification')}
            checked={props.form.watch(
              'intent.options.legacy.insecure_skip_verify'
            )}
            onCheckedChange={(value) =>
              props.form.setValue(
                'intent.options.legacy.insecure_skip_verify',
                value,
                { shouldDirty: true }
              )
            }
          />
          <SettingsSwitchField
            label={t('Force LOGIN authentication')}
            checked={props.form.watch('intent.options.legacy.force_login')}
            onCheckedChange={(value) =>
              props.form.setValue('intent.options.legacy.force_login', value, {
                shouldDirty: true,
              })
            }
          />
        </>
      )}
      <FormField
        control={props.form.control}
        name={secret}
        render={({ field }) => (
          <FormItem>
            <FormLabel>
              {props.microsoft
                ? t('Application secret')
                : t('SMTP password or authorization code')}
            </FormLabel>
            <FormControl>
              <PasswordInput
                {...field}
                autoComplete='new-password'
                onChange={(event) => {
                  field.onChange(event)
                  props.form.setValue(
                    action,
                    event.target.value ? 'replace' : 'keep',
                    { shouldDirty: true }
                  )
                }}
              />
            </FormControl>
            <FormDescription>
              {configured
                ? t('Credential configured. Leave blank to keep it.')
                : t('No credential configured.')}
              {cleared && ` ${t('Credential will be cleared when you save.')}`}
            </FormDescription>
            <FormMessage />
            <div className='flex gap-2'>
              <Button
                type='button'
                variant='outline'
                size='sm'
                disabled={
                  !configured ||
                  (props.microsoft &&
                    props.form.watch('intent.options.auth_mode') ===
                      'microsoft_oauth2_app')
                }
                onClick={() => props.onClear(secret)}
              >
                {t('Clear saved credential')}
              </Button>
              {cleared && (
                <Button
                  type='button'
                  variant='ghost'
                  size='sm'
                  onClick={() =>
                    props.form.setValue(action, 'keep', { shouldDirty: true })
                  }
                >
                  {t('Cancel')}
                </Button>
              )}
            </div>
          </FormItem>
        )}
      />
      {props.microsoft === false && props.settings.client_secret_configured && (
        <div className='space-y-2'>
          <Button
            type='button'
            variant='outline'
            onClick={() => props.onClear('client_secret')}
          >
            {t('Clear saved Microsoft application secret')}
          </Button>
          {props.form.watch('intent.client_secret_action') === 'clear' && (
            <div className='flex items-center gap-2 text-sm'>
              <p>{t('Credential will be cleared when you save.')}</p>
              <Button
                type='button'
                variant='ghost'
                onClick={() =>
                  props.form.setValue('intent.client_secret_action', 'keep', {
                    shouldDirty: true,
                  })
                }
              >
                {t('Cancel')}
              </Button>
            </div>
          )}
        </div>
      )}
    </>
  )
}
