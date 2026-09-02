import { defineResource } from "@/lib/resource";
import custom from "./countries.custom";

export const countryResource = defineResource({
  name: "Country",
  slug: "countries",
  endpoint: "/api/countries",
  icon: "Database",
  label: { singular: "Country", plural: "Countries" },
  table: {
    columns: [
      // grit:cols:auto-start
      { key: "name", label: "Name", sortable: true, searchable: true, onClick: "link" },
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
      // grit:fields:auto-end
    ],
  },
  dashboard: {
    widgets: [
      {
        type: "stat",
        label: "Total Countries",
        endpoint: "/api/countries",
        icon: "Database",
        color: "accent",
      },
    ],
  },
}, custom);
