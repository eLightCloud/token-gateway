import { z } from 'zod'

export const resellerCustomerCreateSchema = z.object({
  username: z.string().trim().min(1, 'Required').max(20, 'Value is too long'),
  display_name: z.string().max(20, 'Value is too long'),
  email: z
    .string()
    .trim()
    .min(1, 'Required')
    .max(50, 'Value is too long')
    .pipe(
      z.email({
        pattern: z.regexes.html5Email,
        error: 'Enter a valid email address',
      })
    ),
})

export const resellerAgentCreateSchema = resellerCustomerCreateSchema.extend({
  phone: z
    .string()
    .trim()
    .min(1, 'Required')
    .regex(/^\+?[0-9]{7,15}$/, 'Enter 7–15 digits with an optional leading +'),
  remark: z.string().trim().max(255, 'Value is too long'),
})

export type ResellerCreateValues = z.infer<
  typeof resellerCustomerCreateSchema
> &
  Partial<Pick<z.infer<typeof resellerAgentCreateSchema>, 'phone' | 'remark'>>
