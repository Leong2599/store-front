import { z } from "zod";

export const CreateStateSchema = z.object({
  name: z.string().min(1, "Required"),
  country_id: z.string().uuid("Invalid ID"),
});

export const UpdateStateSchema = z.object({
  name: z.string().min(1, "Required").optional(),
  country_id: z.string().uuid("Invalid ID").optional(),
});

export type CreateStateInput = z.infer<typeof CreateStateSchema>;
export type UpdateStateInput = z.infer<typeof UpdateStateSchema>;
