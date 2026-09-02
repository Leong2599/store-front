"use client";

import { ResourcePage } from "@/components/resource/resource-page";
import { countryResource } from "@/resources/countries/countries";

export default function CountriesPage() {
  return <ResourcePage resource={countryResource} />;
}
