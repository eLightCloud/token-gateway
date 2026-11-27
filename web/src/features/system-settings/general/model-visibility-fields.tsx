import { Controller, useFormContext } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import { SettingsSwitchField } from '../components/settings-form-layout'
import type { ModelVisibilityValues } from './model-visibility-options'

export function ModelVisibilityFields() {
  const { t } = useTranslation()
  const { control } = useFormContext<ModelVisibilityValues>()

  return (
    <>
      <Controller
        control={control}
        name='domesticModelsOnly'
        render={({ field }) => (
          <SettingsSwitchField
            controlId='model-visibility-domestic-only'
            label={t('Show domestic models only')}
            description={t(
              'Applies only to visitors who are signed out. Common vendors are classified automatically. To make adjustments, open Vendor regions in Model management.'
            )}
            checked={field.value}
            onCheckedChange={field.onChange}
          />
        )}
      />
      <Controller
        control={control}
        name='discountedModelsOnly'
        render={({ field }) => (
          <SettingsSwitchField
            controlId='model-visibility-discounted-only'
            label={t('Show discounted models only')}
            description={t('Applies only to signed-in customer accounts.')}
            checked={field.value}
            onCheckedChange={field.onChange}
          />
        )}
      />
    </>
  )
}
