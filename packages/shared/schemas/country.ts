import { z } from "zod";

export const CreateCountrySchema = z.object({
  name: z.string().min(1, "Required"),
});

export const UpdateCountrySchema = z.object({
  name: z.string().min(1, "Required").optional(),
});

export type CreateCountryInput = z.infer<typeof CreateCountrySchema>;
export type UpdateCountryInput = z.infer<typeof UpdateCountrySchema>;
