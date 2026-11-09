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
import { z } from 'zod'

export const ruleSchema = z.object({
  model: z.string().min(1),
  disabled: z.boolean().optional(),
  ratio: z
    .string()
    .regex(/^(?:[0-4](?:\.[0-9]{1,2})?|5(?:\.0{1,2})?)$/)
    .refine((value) => Number(value) > 0),
  channel_ids: z
    .array(z.number().int().positive())
    .min(1)
    .refine((ids) => new Set(ids).size === ids.length),
})
