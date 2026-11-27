import * as z from 'zod'

export const modelVisibilitySchema = z.object({
  domesticModelsOnly: z.boolean(),
  discountedModelsOnly: z.boolean(),
})

export type ModelVisibilityValues = z.infer<typeof modelVisibilitySchema>

export const modelVisibilityOptionKeys = {
  domesticModelsOnly: 'model_visibility.domestic_only',
  discountedModelsOnly: 'model_visibility.discounted_only',
} as const

export const modelVisibilityDefaults = {
  'model_visibility.domestic_only': true,
  'model_visibility.discounted_only': true,
}

export type ModelVisibilityOptions = typeof modelVisibilityDefaults
