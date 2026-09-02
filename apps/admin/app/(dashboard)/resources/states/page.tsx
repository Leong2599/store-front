"use client";

import { ResourcePage } from "@/components/resource/resource-page";
import { stateResource } from "@/resources/states/states";

export default function StatesPage() {
  return <ResourcePage resource={stateResource} />;
}
