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
import { useMutation } from '@tanstack/react-query'
import { useId } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import { Dialog } from '@/components/dialog'
import { Button } from '@/components/ui/button'
import {
  Field,
  FieldDescription,
  FieldError,
  FieldGroup,
  FieldLabel,
} from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { handleServerError } from '@/lib/handle-server-error'

import { resellerPath, resellerRequest } from './api'
import {
  resellerAgentCreateSchema,
  resellerCustomerCreateSchema,
  type ResellerCreateValues,
} from './lib/create-account-schema'

type ResellerCreateDialogProps = {
  admin: boolean
  open: boolean
  onOpenChange: (open: boolean) => void
  onCreated: (userID: number) => void
}

export function ResellerCreateDialog(props: ResellerCreateDialogProps) {
  const { t } = useTranslation()
  const id = useId()
  const form = useForm<ResellerCreateValues>({
    resolver: zodResolver(
      props.admin ? resellerAgentCreateSchema : resellerCustomerCreateSchema
    ),
    defaultValues: {
      username: '',
      display_name: '',
      email: '',
      phone: '',
      remark: '',
    },
  })
  const mutation = useMutation({
    meta: { errorToast: false },
    mutationFn: (values: ResellerCreateValues) =>
      resellerRequest<{ user_id: number }>(
        `${resellerPath(props.admin)}?send_activation_email=false`,
        'post',
        values
      ),
    onError: (error) => handleServerError(error, t('Failed to create account')),
    onSuccess: (result) => {
      form.reset()
      props.onOpenChange(false)
      props.onCreated(result.user_id)
    },
  })

  const fields = [
    { name: 'username', label: t('Username'), maxLength: 20, required: true },
    { name: 'display_name', label: t('Display Name'), maxLength: 20 },
    {
      name: 'email',
      label: t('Email'),
      type: 'email',
      autoComplete: 'email',
      maxLength: 50,
      required: true,
    },
    {
      name: 'phone',
      label: t('Mobile number'),
      type: 'tel',
      autoComplete: 'tel',
      maxLength: 16,
      required: true,
    },
  ] as const
  const { errors } = form.formState
  const remarkID = `${id}-remark`

  return (
    <Dialog
      open={props.open}
      onOpenChange={(open) => {
        if (mutation.isPending) return
        if (!open) form.reset()
        props.onOpenChange(open)
      }}
      title={props.admin ? t('Create reseller') : t('Create customer')}
      description={
        props.admin
          ? t(
              'The account email includes the username and a password setup link. An administrator activates the account separately.'
            )
          : t(
              'An activation email lets the account holder set a password and configure MFA.'
            )
      }
    >
      <form
        noValidate
        onSubmit={form.handleSubmit((values) => mutation.mutate(values))}
      >
        <FieldGroup className='gap-4'>
          {fields.map((field) => {
            if (field.name === 'phone' && !props.admin) return null
            const fieldID = `${id}-${field.name}`
            const error = errors[field.name]?.message
            return (
              <Field key={field.name} data-invalid={!!error}>
                <FieldLabel htmlFor={fieldID}>
                  {field.label}
                  {'required' in field && <span aria-hidden='true'>*</span>}
                </FieldLabel>
                <Input
                  {...form.register(field.name)}
                  id={fieldID}
                  type={'type' in field ? field.type : 'text'}
                  autoComplete={
                    'autoComplete' in field ? field.autoComplete : undefined
                  }
                  maxLength={field.maxLength}
                  required={'required' in field}
                  aria-invalid={!!error}
                  aria-describedby={error ? `${fieldID}-error` : undefined}
                />
                {error && (
                  <FieldError id={`${fieldID}-error`}>{t(error)}</FieldError>
                )}
              </Field>
            )
          })}
          {props.admin && (
            <Field data-invalid={!!errors.remark}>
              <FieldLabel htmlFor={remarkID}>{t('Remark')}</FieldLabel>
              <Textarea
                {...form.register('remark')}
                id={remarkID}
                rows={3}
                maxLength={255}
                aria-invalid={!!errors.remark}
                aria-describedby={
                  errors.remark
                    ? `${remarkID}-description ${remarkID}-error`
                    : `${remarkID}-description`
                }
              />
              <FieldDescription id={`${remarkID}-description`}>
                {t('Admin notes (only visible to admins)')}
              </FieldDescription>
              {errors.remark?.message && (
                <FieldError id={`${remarkID}-error`}>
                  {t(errors.remark.message)}
                </FieldError>
              )}
            </Field>
          )}
          <div>
            <Button type='submit' disabled={mutation.isPending}>
              {t('Create')}
            </Button>
          </div>
        </FieldGroup>
      </form>
    </Dialog>
  )
}
