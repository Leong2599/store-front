import type { Country } from "./country";

export interface State {
  id: string;
  name: string;
  country_id: string;
  country?: Country;
  created_at: string;
  updated_at: string;
}
