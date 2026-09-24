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

interface VideoUpstreamFieldsProps {
  form: UseFormReturn<ChannelFormValues>
  locked: boolean
  channelType: number
  pluginKey?: string
  extensionKeys?: string[]
}

export function VideoUpstreamFields(props: VideoUpstreamFieldsProps) {
  const { t } = useTranslation()
  const protocol = useWatch({
    control: props.form.control,
    name: 'video_upstream_protocol',
  })
  const enabled =
    props.channelType === 54 ||
    props.channelType === 45 ||
    props.pluginKey === 'doubao' ||
    props.extensionKeys?.includes('doubao')
  if (!enabled) return null
  return (
    <>
      <FormField
        control={props.form.control}
        name='video_upstream_protocol'
        render={({ field }) => (
          <FormItem>
            <FormLabel>{t('Video Upstream Protocol')}</FormLabel>
            <Select
              disabled={props.locked}
              items={[
                { value: 'ark', label: t('Ark native (ModelArk)') },
                { value: 'openai_video', label: t('OpenAI Videos compatible') },
              ]}
              value={field.value || 'ark'}
              onValueChange={(value) => {
                const nextProtocol =
                  value === 'openai_video' ? 'openai_video' : 'ark'
                field.onChange(nextProtocol)
                if (nextProtocol !== 'openai_video') {
                  props.form.setValue('video_upstream_profile', 'standard', {
                    shouldDirty: true,
                    shouldValidate: true,
                  })
                }
              }}
            >
              <FormControl>
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
              </FormControl>
              <SelectContent alignItemWithTrigger={false}>
                <SelectGroup>
                  <SelectItem value='ark'>
                    {t('Ark native (ModelArk)')}
                  </SelectItem>
                  <SelectItem value='openai_video'>
                    {t('OpenAI Videos compatible')}
                  </SelectItem>
                </SelectGroup>
              </SelectContent>
            </Select>
            <FormDescription>
              {t(
                'Southbound wire format for video generation. Existing tasks keep the protocol pinned at submission.'
              )}
            </FormDescription>
            <FormMessage />
          </FormItem>
        )}
      />
      <FormField
        control={props.form.control}
        name='video_upstream_profile'
        render={({ field }) => {
          const videosSelected = (protocol || 'ark') === 'openai_video'
          return (
            <FormItem>
              <FormLabel>{t('Video Upstream Profile')}</FormLabel>
              <Select
                disabled={props.locked || !videosSelected}
                items={[
                  { value: 'standard', label: t('Standard Videos') },
                  { value: 'seedance_codeyy', label: 'Seedance · CodeYY' },
                  { value: 'seedance_zapgogo', label: 'Seedance · Zapgogo' },
                ]}
                value={field.value || 'standard'}
                onValueChange={(value) => {
                  field.onChange(
                    value === 'seedance_codeyy' || value === 'seedance_zapgogo'
                      ? value
                      : 'standard'
                  )
                }}
              >
                <FormControl>
                  <SelectTrigger disabled={props.locked || !videosSelected}>
                    <SelectValue />
                  </SelectTrigger>
                </FormControl>
                <SelectContent alignItemWithTrigger={false}>
                  <SelectGroup>
                    <SelectItem value='standard'>
                      {t('Standard Videos')}
                    </SelectItem>
                    <SelectItem value='seedance_codeyy'>
                      Seedance · CodeYY
                    </SelectItem>
                    <SelectItem value='seedance_zapgogo'>
                      Seedance · Zapgogo
                    </SelectItem>
                  </SelectGroup>
                </SelectContent>
              </Select>
              <FormDescription>
                {videosSelected
                  ? t(
                      'Vendor extension contract on top of the OpenAI Videos protocol.'
                    )
                  : t(
                      'Vendor extension profiles apply only to the OpenAI Videos protocol.'
                    )}
              </FormDescription>
              <FormMessage />
            </FormItem>
          )
        }}
      />
    </>
  )
}
