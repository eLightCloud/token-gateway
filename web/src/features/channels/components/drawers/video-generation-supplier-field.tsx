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

import {
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'

import type { ChannelFormValues } from '../../lib/channel-form'

// Keep the existing storage field as the single source of supplier identity.
// It belongs to the channel connection even while enhancement is disabled.
export function VideoGenerationSupplierField(props: {
  form: UseFormReturn<ChannelFormValues>
  locked: boolean
}) {
  const { t } = useTranslation()
  return (
    <FormField
      control={props.form.control}
      name='video_enhance.supplier'
      render={({ field }) => (
        <FormItem>
          <FormLabel>{t('Channel video generation supplier')}</FormLabel>
          <Select
            disabled={props.locked}
            value={field.value || null}
            items={[
              { value: 'volcengine', label: t('Volcengine official') },
              { value: 'runyyitd', label: 'runyyitd' },
            ]}
            onValueChange={(value) => {
              props.form.setValue('video_enhance.supplier', value ?? '', {
                shouldDirty: true,
                shouldValidate: true,
              })
            }}
          >
            <FormControl>
              <SelectTrigger ref={field.ref} onBlur={field.onBlur}>
                <SelectValue placeholder={t('Select a supplier')} />
              </SelectTrigger>
            </FormControl>
            <SelectContent>
              <SelectGroup>
                <SelectItem value='volcengine'>
                  {t('Volcengine official')}
                </SelectItem>
                <SelectItem value='runyyitd'>runyyitd</SelectItem>
              </SelectGroup>
            </SelectContent>
          </Select>
          <FormDescription>
            {t(
              'Save the supplier once for this channel. Enhancement reuses this setting.'
            )}
          </FormDescription>
          <FormMessage />
        </FormItem>
      )}
    />
  )
}
