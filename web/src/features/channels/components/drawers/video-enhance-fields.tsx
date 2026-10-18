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
import { useWatch, type UseFormReturn } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import {
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
} from '@/components/ui/form'
import { Switch } from '@/components/ui/switch'

import type { ChannelFormValues } from '../../lib/channel-form'
import {
  getVideoEnhanceBaseModels,
  videoEnhanceChannelSupported,
} from '../../lib/video-enhance'

export function VideoEnhanceFields(props: {
  form: UseFormReturn<ChannelFormValues>
  locked: boolean
  channelType: number
  pluginKey?: string
  extensionKeys?: string[]
}) {
  const { t } = useTranslation()
  const enabled = useWatch({
    control: props.form.control,
    name: 'video_enhance.enabled',
  })
  const supplier = useWatch({
    control: props.form.control,
    name: 'video_enhance.supplier',
  })
  const models = useWatch({ control: props.form.control, name: 'models' }) || ''
  if (
    !videoEnhanceChannelSupported(
      props.channelType,
      props.pluginKey,
      props.extensionKeys
    )
  ) {
    return null
  }
  const baseModels = getVideoEnhanceBaseModels(models.split(','))
  const supplierLabel =
    supplier === 'volcengine' ? t('Volcengine official') : supplier
  return (
    <div
      role='group'
      aria-label={t('Seedance video enhancement')}
      className='space-y-4 border-t pt-4'
    >
      <FormField
        control={props.form.control}
        name='video_enhance.enabled'
        render={({ field }) => (
          <FormItem className='flex items-center justify-between gap-4'>
            <div>
              <FormLabel>{t('Seedance video enhancement')}</FormLabel>
              <FormDescription>
                {t(
                  'Keep base models and add an enhance model billed by final video seconds.'
                )}
              </FormDescription>
            </div>
            <FormControl>
              <Switch
                disabled={props.locked}
                checked={field.value ?? false}
                onCheckedChange={field.onChange}
              />
            </FormControl>
          </FormItem>
        )}
      />
      {enabled && (
        <>
          <p className='text-sm'>
            {supplier
              ? t(
                  'Generation uses this channel ({{supplier}}); enhancement uses MediaKit.',
                  {
                    supplier: supplierLabel,
                  }
                )
              : t(
                  "Select this channel's video generation supplier before enabling enhancement."
                )}
          </p>
          <p className='text-muted-foreground text-xs'>
            {t(
              'Uses MediaKit large-model enhancement. Output resolution: 1080p or 2K. Configure MediaKit, OSS and model pricing before use.'
            )}
          </p>
          {baseModels.length === 0 ? (
            <p role='status' className='text-sm'>
              {t(
                'Add a supported Seedance 2.0 or 2.5 base model to publish enhancement models.'
              )}
            </p>
          ) : (
            <ul className='space-y-1 text-xs'>
              {baseModels.map((model) => (
                <li className='break-all' key={model}>
                  {model} → {model}-enhance
                </li>
              ))}
            </ul>
          )}
        </>
      )}
    </div>
  )
}
