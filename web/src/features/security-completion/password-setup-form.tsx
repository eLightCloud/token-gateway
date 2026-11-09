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

import {
  passwordSetupSchema,
  type PasswordSetupValues,
} from './lib/password-setup'

type PasswordSetupFormProps = {
  pending: boolean
  onConfirm: (password: string) => void
}

export function PasswordSetupForm(props: PasswordSetupFormProps) {
  const { t } = useTranslation()
  const form = useForm<PasswordSetupValues>({
    resolver: zodResolver(passwordSetupSchema),
    defaultValues: { password: '', confirmPassword: '' },
  })

  return (
    <Form {...form}>
      <form
        className='space-y-4'
        onSubmit={form.handleSubmit((values) => {
          if (!props.pending) props.onConfirm(values.password)
        })}
      >
        <FormField
          control={form.control}
          name='password'
          render={({ field }) => (
            <FormItem>
              <FormLabel>{t('Password')}</FormLabel>
              <FormControl>
                <PasswordInput
                  {...field}
                  autoComplete='new-password'
                  disabled={props.pending}
                />
              </FormControl>
              <FormMessage />
            </FormItem>
          )}
        />
        <FormField
          control={form.control}
          name='confirmPassword'
          render={({ field }) => (
            <FormItem>
              <FormLabel>{t('Confirm password')}</FormLabel>
              <FormControl>
                <PasswordInput
                  {...field}
                  autoComplete='new-password'
                  disabled={props.pending}
                />
              </FormControl>
              <FormMessage />
            </FormItem>
          )}
        />
        <Button type='submit' disabled={props.pending}>
          {t('Confirm')}
        </Button>
      </form>
    </Form>
  )
}
