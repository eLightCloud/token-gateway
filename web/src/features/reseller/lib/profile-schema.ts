import { z } from 'zod'

import { resellerCreateSchema } from './create-account-schema'

export const resellerProfileSchema = z.object({
  display_name: z
    .string()
    .trim()
    .refine((name) => [...name].length <= 20, 'Value is too long'),
  phone: z
    .string()
    .trim()
    .pipe(
      z.union([resellerCreateSchema.shape.phone, z.literal('')], {
        error: 'Enter 7–15 digits with an optional leading +',
      })
    ),
})

export type ResellerProfileValues = z.infer<typeof resellerProfileSchema>

export const pendingResellerEmailSchema = z.object({
  email: z
    .string()
    .trim()
    .pipe(
      z.union([resellerCreateSchema.shape.email, z.literal('')], {
        error: 'Enter a valid email address',
      })
    ),
})

export type PendingResellerEmail = z.infer<typeof pendingResellerEmailSchema>
