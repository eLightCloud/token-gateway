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
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { z } from 'zod'

import { Dialog } from '@/components/dialog'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import {
  Form,
  FormControl,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { SecureVerificationDialog } from '@/features/auth/secure-verification'
import type { AccountSecurityResult } from '@/features/profile/types'
import { useAccountSecurity } from '@/features/security/hooks/use-account-security'
import { api } from '@/lib/api'
import { authRequestOptions, authResult } from '@/lib/secure-verification'

const schema = z.object({
  email: z
    .string()
    .email('Please enter a valid email address')
    .or(z.literal('')),
  incident: z
    .string()
    .min(3, 'Enter an incident reference.')
    .max(100, 'Enter an incident reference.'),
  evidence: z
    .string()
    .min(20, 'Record two independent identity checks.')
    .max(2000, 'Record two independent identity checks.'),
  revoke_keys: z.boolean(),
  confirmed: z
    .boolean()
    .refine((value) => value, 'Confirm the identity checks before recovery.'),
})

interface RecoveryDialogProps {
  emailChange?: boolean
  open: boolean
  onOpenChange: (open: boolean) => void
  userID: number
  onSuccess: () => void
}

export function SecurityRecoveryDialog(props: RecoveryDialogProps) {
  const { t } = useTranslation()
  const security = useAccountSecurity()
  const form = useForm<z.infer<typeof schema>>({
    resolver: zodResolver(schema),
    defaultValues: {
      email: '',
      incident: '',
      evidence: '',
      revoke_keys: false,
      confirmed: false,
    },
  })
  const submit = form.handleSubmit(async (values) => {
    if (props.emailChange && !values.email) {
      form.setError('email', { message: 'Please enter a valid email address' })
      return
    }
    const result = await security.run(async (signal) => {
      const proof = await security.verify(
        props.emailChange
          ? {
              scope: 'account.security.email_change',
              context: {
                user_id: props.userID,
                incident: values.incident,
                email: values.email.trim().toLowerCase(),
              },
            }
          : {
              scope: 'account.security.recover',
              context: {
                user_id: props.userID,
                incident: values.incident,
                revoke_keys: values.revoke_keys,
              },
            },
        signal
      )
      return authResult<AccountSecurityResult>(
        api.post(
          `/api/security/admin/users/${props.userID}/${props.emailChange ? 'email-change' : 'recovery'}`,
          values,
          {
            ...authRequestOptions,
            headers: { 'X-Security-Proof': proof },
            signal,
            singleUseAuthorization: true,
          }
        )
      )
    })
    if (result) {
      props.onOpenChange(false)
      props.onSuccess()
    }
  })
  return (
    <>
      <Dialog
        open={props.open && !security.showVerification}
        onOpenChange={(open) => {
          if (!open) security.cancel()
          props.onOpenChange(open)
        }}
        title={
          props.emailChange
            ? t('Request email change')
            : t('Account security recovery')
        }
        description={t(
          'Record verification references only. Never enter passwords, codes, tokens or identity documents.'
        )}
      >
        <Form {...form}>
          <form className='space-y-4' onSubmit={submit}>
            {props.emailChange && (
              <FormField
                control={form.control}
                name='email'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('New Email')}</FormLabel>
                    <FormControl>
                      <Input {...field} type='email' autoComplete='email' />
                    </FormControl>
                    <FormMessage />
                  </FormItem>
                )}
              />
            )}
            <FormField
              control={form.control}
              name='incident'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Incident reference')}</FormLabel>
                  <FormControl>
                    <Input {...field} />
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )}
            />
            <FormField
              control={form.control}
              name='evidence'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Identity verification references')}</FormLabel>
                  <FormControl>
                    <Input {...field} />
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )}
            />
            <FormField
              control={form.control}
              name='confirmed'
              render={({ field }) => (
                <FormItem className='flex flex-wrap items-center gap-2'>
                  <FormControl>
                    <Checkbox
                      checked={field.value}
                      onCheckedChange={(value) =>
                        field.onChange(value === true)
                      }
                    />
                  </FormControl>
                  <FormLabel>
                    {t('I completed two independent identity checks.')}
                  </FormLabel>
                  <FormMessage />
                </FormItem>
              )}
            />
            {!props.emailChange && (
              <FormField
                control={form.control}
                name='revoke_keys'
                render={({ field }) => (
                  <FormItem className='flex flex-wrap items-center gap-2'>
                    <FormControl>
                      <Checkbox
                        checked={field.value}
                        onCheckedChange={(value) =>
                          field.onChange(value === true)
                        }
                      />
                    </FormControl>
                    <FormLabel>
                      {t('Confirmed key compromise: revoke consumption keys')}
                    </FormLabel>
                    <FormMessage />
                  </FormItem>
                )}
              />
            )}
            <p>
              {props.emailChange
                ? t(
                    'The current email remains unchanged until the new address is verified.'
                  )
                : t(
                    'All browser sessions will be revoked. Consumption keys remain valid unless compromise is selected.'
                  )}
            </p>
            <Button type='submit' disabled={security.pending}>
              {props.emailChange ? t('Request email change') : t('Reset 2FA')}
            </Button>
          </form>
        </Form>
      </Dialog>
      <SecureVerificationDialog {...security.verificationDialogProps} />
    </>
  )
}
