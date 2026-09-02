import { defineResource } from "@/lib/resource";
import custom from "./states.custom";

export const stateResource = defineResource({
  name: "State",
  slug: "states",
  endpoint: "/api/states",
  icon: "Database",
  label: { singular: "State", plural: "States" },
  table: {
    columns: [
      // grit:cols:auto-start
      { key: "name", label: "Name", sortable: true, searchable: true, onClick: "link" },
      { key: "country.name", label: "Country" },
      { key: "created_at", label: "Created", sortable: true, format: "relative" },
      // grit:cols:auto-end
    ],
    filters: [
    ],
    defaultSort: { key: "created_at", direction: "desc" },
    searchable: true,
    pageSize: 20,
    // Shown once rows are ticked. Drop "archive" here and the Archived tab
    // goes with it; the model keeps its archived_at either way.
    bulkActions: ["edit", "archive", "restore", "export", "delete"],
  },
  form: {
    fields: [
      // grit:fields:auto-start
    { key: "name", label: "Name", type: "text", required: true },
    { key: "country_id", label: "Country", type: "relationship-select", required: true, relatedEndpoint: "/api/countries", displayField: "name" },
      // grit:fields:auto-end
    ],
  },
  dashboard: {
    widgets: [
      {
        type: "stat",
        label: "Total States",
        endpoint: "/api/states",
        icon: "Database",
        color: "accent",
      },
    ],
  },
}, custom);
