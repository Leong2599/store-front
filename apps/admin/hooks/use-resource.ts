import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { apiClient } from "@/lib/api-client";

interface ResourceQueryParams {
  page?: number;
  pageSize?: number;
  search?: string;
  sortBy?: string;
  sortOrder?: "asc" | "desc";
  filters?: Record<string, string>;
  // v3.31.34 — date-window filter. dateParams comes from
  // dateRangeToQueryParams(); dateField overrides the server's
  // default "created_at" target column when set.
  dateParams?: Record<string, string>;
  dateField?: string;
}

interface PaginatedResponse<T = Record<string, unknown>> {
  data: T[];
  meta: {
    total: number;
    page: number;
    page_size: number;
    pages: number;
  };
}

export function useResource<T = Record<string, unknown>>(
  endpoint: string,
  params: ResourceQueryParams = {}
) {
  const { page = 1, pageSize = 20, search, sortBy, sortOrder, filters, dateParams, dateField } = params;

  return useQuery<PaginatedResponse<T>>({
    // v3.31.34: dateParams + dateField included in key so a date
    // filter change invalidates the cache and the list refetches.
    queryKey: [endpoint, { page, pageSize, search, sortBy, sortOrder, filters, dateParams, dateField }],
    queryFn: async () => {
      const searchParams = new URLSearchParams({
        page: String(page),
        page_size: String(pageSize),
      });

      if (search) searchParams.set("search", search);
      if (sortBy) {
        searchParams.set("sort_by", sortBy);
        searchParams.set("sort_order", sortOrder ?? "desc");
      }
      if (filters) {
        Object.entries(filters).forEach(([key, value]) => {
          if (value) searchParams.set(key, value);
        });
      }
      if (dateParams) {
        Object.entries(dateParams).forEach(([key, value]) => {
          if (value) searchParams.set(key, value);
        });
      }
      if (dateField && dateField !== "created_at") {
        searchParams.set("date_field", dateField);
      }

      const { data } = await apiClient.get(`${endpoint}?${searchParams}`);
      return data;
    },
  });
}

export function useResourceItem<T = Record<string, unknown>>(
  endpoint: string,
  id: string,
  options?: { enabled?: boolean }
) {
  return useQuery<{ data: T }>({
    queryKey: [endpoint, id],
    queryFn: async () => {
      const { data } = await apiClient.get(`${endpoint}/${id}`);
      return data;
    },
    enabled: (options?.enabled ?? true) && !!id,
  });
}

// Every mutation hook takes an optional resource label (the singular, e.g.
// "Invoice") so toasts name what actually happened — "Invoice created
// successfully" rather than a bare "Created successfully". Omitting it keeps
// the old generic wording, so existing call sites still compile.
function said(label: string | undefined, verb: string) {
  return label ? label + " " + verb : verb.charAt(0).toUpperCase() + verb.slice(1);
}

function failed(label: string | undefined, verb: string) {
  return label ? "Failed to " + verb + " " + label.toLowerCase() : "Failed to " + verb;
}

export function useCreateResource(endpoint: string, label?: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: async (body: Record<string, unknown>) => {
      const { data } = await apiClient.post(endpoint, body);
      return data;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: [endpoint] });
      toast.success(said(label, "created successfully"));
    },
    onError: (err: unknown) => {
      const axiosErr = err as { response?: { data?: { error?: { message?: string } } } };
      toast.error(axiosErr?.response?.data?.error?.message || failed(label, "create"));
    },
  });
}

export function useUpdateResource(endpoint: string, label?: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: async ({ id, body }: { id: string; body: Record<string, unknown> }) => {
      const { data } = await apiClient.put(`${endpoint}/${id}`, body);
      return data;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: [endpoint] });
      toast.success(said(label, "updated successfully"));
    },
    onError: (err: unknown) => {
      const axiosErr = err as { response?: { data?: { error?: { message?: string } } } };
      toast.error(axiosErr?.response?.data?.error?.message || failed(label, "update"));
    },
  });
}

// v3.31.18: partial updates for the grouped update view. Each group's
// Save button calls patch() with only the fields it owns. The Go-side
// Patch handler whitelists writable columns and silently drops anything
// else, so it's safe to send only a subset.
export function usePatchResource(endpoint: string, label?: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: async ({ id, body }: { id: string; body: Record<string, unknown> }) => {
      const { data } = await apiClient.patch(`${endpoint}/${id}`, body);
      return data;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: [endpoint] });
      toast.success(label ? label + " saved" : "Saved");
    },
    onError: (err: unknown) => {
      const axiosErr = err as { response?: { data?: { error?: { message?: string } } } };
      toast.error(axiosErr?.response?.data?.error?.message || failed(label, "save"));
    },
  });
}

export function useDeleteResource(endpoint: string, label?: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: async (id: string) => {
      await apiClient.delete(`${endpoint}/${id}`);
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: [endpoint] });
      toast.success(said(label, "deleted successfully"));
    },
    onError: (err: unknown) => {
      const axiosErr = err as { response?: { data?: { error?: { message?: string } } } };
      toast.error(axiosErr?.response?.data?.error?.message || failed(label, "delete"));
    },
  });
}

export type BulkOperation = "delete" | "archive" | "restore" | "patch";

export interface BulkPayload {
  action: BulkOperation;
  ids: string[];
  /** Only read for "patch". */
  patch?: Record<string, unknown>;
}

const BULK_PAST: Record<BulkOperation, string> = {
  delete: "deleted",
  archive: "archived",
  restore: "restored",
  patch: "updated",
};

/**
 * One request for the whole selection, against POST <endpoint>/bulk.
 *
 * This used to be N parallel DELETEs, which is N transactions and N audit
 * entries, and leaves a half-applied result when the eleventh fails: the
 * operator is told it failed while ten rows are already gone. The server does
 * it in one transaction now, so the answer is all or nothing.
 *
 * Takes the PLURAL label ("Invoices") because the message counts rows, and
 * reports what the server actually did rather than what was asked: archiving
 * twelve rows of which three were already archived says nine.
 */
export function useBulkResource(endpoint: string, pluralLabel?: string, singularLabel?: string) {
  const queryClient = useQueryClient();

  return useMutation({
    // ids are strings because Grit's models use UUID primary keys.
    mutationFn: async (payload: BulkPayload) => {
      try {
        const { data } = await apiClient.post(`${endpoint}/bulk`, payload);
        return { ...data, action: payload.action } as {
          data?: { affected: number; requested: number };
          action: BulkOperation;
        };
      } catch (err) {
        // No /bulk route on this endpoint. That is the normal state of an
        // upgraded project: grit upgrade replaces the admin but never
        // regenerates API handlers, so the browser gets the new code and the
        // server keeps the old routes. Falling back per row keeps the button
        // working instead of 404ing on every existing install.
        //
        // The fallback is genuinely worse: N requests, N transactions, and a
        // partial result if one fails. Run grit generate for the resource to
        // get the real endpoint.
        const status = (err as { response?: { status?: number } })?.response?.status;
        if (status !== 404) throw err;

        const results = await Promise.allSettled(
          payload.ids.map((id) =>
            payload.action === "delete"
              ? apiClient.delete(`${endpoint}/${id}`)
              : apiClient.patch(`${endpoint}/${id}`, payload.patch ?? {}),
          ),
        );
        const affected = results.filter((r) => r.status === "fulfilled").length;
        if (affected === 0) throw err;
        return {
          data: { affected, requested: payload.ids.length },
          action: payload.action,
        };
      }
    },
    onSuccess: (result) => {
      queryClient.invalidateQueries({ queryKey: [endpoint] });
      const affected = result?.data?.affected ?? 0;
      const requested = result?.data?.requested ?? affected;
      const noun = affected === 1 ? (singularLabel ?? "item") : (pluralLabel ?? "items");

      if (affected === 0) {
        // Not a success worth celebrating and not an error either. Saying
        // "0 archived" beats a green tick over a table that did not change.
        toast("Nothing to " + result.action + ": no matching rows");
        return;
      }
      const skipped = requested - affected;
      toast.success(
        affected + " " + noun + " " + BULK_PAST[result.action] +
          (skipped > 0 ? " (" + skipped + " already were)" : "")
      );
    },
    onError: (err: unknown) => {
      const axiosErr = err as { response?: { data?: { error?: { message?: string } } } };
      toast.error(
        axiosErr?.response?.data?.error?.message ||
          "Bulk action failed. Nothing was changed."
      );
    },
  });
}

/**
 * Kept so existing call sites and hand-written pages keep working. Delegates
 * to the bulk endpoint rather than firing one request per row.
 *
 * @deprecated Use useBulkResource, which also archives, restores and patches.
 */
export function useBulkDeleteResource(endpoint: string, pluralLabel?: string) {
  const bulk = useBulkResource(endpoint, pluralLabel);
  return {
    ...bulk,
    mutate: (ids: string[], options?: Parameters<typeof bulk.mutate>[1]) =>
      bulk.mutate({ action: "delete", ids }, options),
  };
}
