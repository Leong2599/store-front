"use client";

import { useCallback, useMemo, useState } from "react";
import {
  usePathname,
  useRouter,
  useSearchParams,
  type ReadonlyURLSearchParams,
} from "next/navigation";
import { useQueryClient } from "@tanstack/react-query";
import type {
  BulkAction,
  ColumnDefinition,
  CustomBulkAction,
  ResourceDefinition,
  TableAction,
  TableTab,
} from "@/lib/resource";
import {
  useBulkResource,
  useDeleteResource,
  useResource,
} from "@/hooks/use-resource";
import type { StatCard } from "@/components/layout/page-header";
import { dateRangeToQueryParams, type DateRange } from "@/components/tables/date-filter";

// Read the date filter back out of the address bar so a refresh or a shared
// link rehydrates the same view.
function readDateRangeFromURL(sp: ReadonlyURLSearchParams | null): DateRange {
  if (!sp) return {};
  const preset = sp.get("date") as DateRange["preset"] | null;
  if (preset === "custom") {
    return {
      preset: "custom",
      from: sp.get("date_from") ?? undefined,
      to: sp.get("date_to") ?? undefined,
    };
  }
  if (preset === "today" || preset === "7d" || preset === "30d" || preset === "month") {
    return { preset };
  }
  return {};
}

// replace, not push: the back button should not collect one entry per filter
// tweak.
function writeDateRangeToURL(
  router: ReturnType<typeof useRouter>,
  pathname: string,
  current: ReadonlyURLSearchParams | null,
  range: DateRange,
) {
  const params = new URLSearchParams(current?.toString() ?? "");
  params.delete("date");
  params.delete("date_from");
  params.delete("date_to");
  if (range.preset) {
    params.set("date", range.preset);
    if (range.preset === "custom") {
      if (range.from) params.set("date_from", range.from);
      if (range.to) params.set("date_to", range.to);
    }
  }
  const qs = params.toString();
  router.replace(qs ? pathname + "?" + qs : pathname, { scroll: false });
}

export interface ResourceControllerOptions {
  /** Start on a page other than 1. */
  initialPage?: number;
  /** Override the resource's configured page size. */
  initialPageSize?: number;
}

export interface ResourceController<T = Record<string, unknown>> {
  resource: ResourceDefinition;

  // ── data ────────────────────────────────────────────────────────────
  rows: T[];
  meta: { total: number; page: number; page_size: number; pages: number } | undefined;
  total: number;
  totalPages: number;
  isLoading: boolean;

  // ── query state (all of it URL- or server-aware) ────────────────────
  page: number;
  pageSize: number;
  search: string;
  sortBy: string;
  sortOrder: "asc" | "desc";
  filters: Record<string, string>;
  dateRange: DateRange;
  setPage: (page: number) => void;
  setPageSize: (size: number) => void;
  setSearch: (value: string) => void;
  /** Toggles direction when the same key is passed twice. */
  setSort: (key: string) => void;
  setFilter: (key: string, value: string) => void;
  setDateRange: (range: DateRange) => void;

  // ── columns ─────────────────────────────────────────────────────────
  /** resource.table.columns minus hidden ones — what a table should render. */
  columns: ColumnDefinition[];
  allColumns: ColumnDefinition[];
  hiddenColumns: string[];
  toggleColumn: (key: string) => void;

  // ── selection ───────────────────────────────────────────────────────
  selection: string[];
  setSelection: (ids: string[]) => void;
  clearSelection: () => void;

  // ── actions ─────────────────────────────────────────────────────────
  actions: TableAction[];
  can: (action: TableAction) => boolean;
  create: () => void;
  /** Create with fields pre-filled, e.g. createWith({ parent_id: id }). */
  createWith: (defaults: Record<string, unknown>) => void;
  edit: (row: T) => void;
  view: (row: T) => void;
  /** Opens the confirm dialog; deletion happens on confirm. */
  remove: (id: string) => void;
  bulkRemove: () => void;
  isDeleting: boolean;
  isBulkDeleting: boolean;

  // ── bulk actions ────────────────────────────────────────────────────
  /** Built-ins the resource has switched on, minus any that make no sense
   *  in the current view (restore only appears while Archived is open). */
  bulkActions: BulkAction[];
  /** Custom ones from resources/<name>.custom.tsx, already filtered by visible(). */
  customBulkActions: CustomBulkAction<T>[];
  /** The rows behind the current selection, readable without a refetch. */
  selectedRows: T[];
  /** Opens the confirm dialog; archiving happens on confirm. */
  bulkArchive: () => void;
  /** Restores immediately: putting something back is not destructive. */
  bulkRestore: () => void;
  /** Opens the bulk edit dialog. */
  bulkEdit: () => void;
  /** Writes one field to every selected row and closes the dialog. */
  applyBulkEdit: (patch: Record<string, unknown>) => void;
  /** Runs a custom action, handing it the ids, the rows and the helpers. */
  runBulkAction: (action: CustomBulkAction<T>) => void;
  isBulkPending: boolean;
  /** Re-runs the list query. Handed to custom actions so they can refresh. */
  refresh: () => void;
  /** Speaks to the page's live region. Bulk changes never move focus. */
  announce: (message: string) => void;
  liveMessage: string;

  // ── tabs ────────────────────────────────────────────────────────────
  /** The resource's filter presets, or an empty array when it has none. */
  tabs: TableTab[];
  /** Key of the tab currently applied. "" when the resource has no tabs. */
  activeTab: string;
  setActiveTab: (key: string) => void;

  // ── archived view ───────────────────────────────────────────────────
  /** True while the Archived tab is open. */
  showArchived: boolean;
  setShowArchived: (value: boolean) => void;

  // ── dialog state, for anyone rendering their own ────────────────────
  form: {
    open: boolean;
    item: T | null;
    /** Pre-filled values for create mode, set by createWith(). */
    defaults?: Record<string, unknown>;
    close: () => void;
  };
  confirmDelete: { open: boolean; confirm: () => void; cancel: () => void };
  confirmBulkDelete: { open: boolean; confirm: () => void; cancel: () => void };
  confirmBulkArchive: { open: boolean; confirm: () => void; cancel: () => void };
  bulkEditor: { open: boolean; close: () => void };
  /** Set when a custom action asked to confirm first. */
  confirmCustom: {
    open: boolean;
    action: CustomBulkAction<T> | null;
    confirm: () => void;
    cancel: () => void;
  };
  importer: { open: boolean; setOpen: (open: boolean) => void };

  // ── odds and ends the default page needs ────────────────────────────
  /** Same query the table ran, for an export that matches what is on screen. */
  apiSearchParams: URLSearchParams;
  stats: StatCard[] | undefined;
  singularName: string;
  pluralName: string;
  isFormPage: boolean;
  isSteps: boolean;
}

/**
 * Everything a resource list page needs except the markup.
 *
 * const c = useResourceController(productsResource)
 * <MyTable rows={c.rows} onSort={c.setSort} onRowClick={c.edit} />
 */
export function useResourceController<T = Record<string, unknown>>(
  resource: ResourceDefinition,
  options: ResourceControllerOptions = {},
): ResourceController<T> {
  const router = useRouter();
  const pathname = usePathname();
  const searchParams = useSearchParams();

  const isFormPage = resource.formView === "page" || resource.formView === "page-steps";
  const isSteps = resource.formView === "modal-steps" || resource.formView === "page-steps";

  const [page, setPage] = useState(options.initialPage ?? 1);
  const [pageSize, setPageSizeState] = useState(
    options.initialPageSize ?? resource.table.pageSize ?? 20,
  );
  const [search, setSearchState] = useState("");
  const [sortBy, setSortBy] = useState(resource.table.defaultSort?.key ?? "");
  const [sortOrder, setSortOrder] = useState<"asc" | "desc">(
    resource.table.defaultSort?.direction ?? "desc",
  );
  const [selection, setSelection] = useState<string[]>([]);
  const [filters, setFilters] = useState<Record<string, string>>({});
  const [hiddenColumns, setHiddenColumns] = useState<string[]>([]);

  const [dateRange, setDateRangeState] = useState<DateRange>(() =>
    readDateRangeFromURL(searchParams),
  );
  const dateParams = useMemo(() => dateRangeToQueryParams(dateRange), [dateRange]);
  const setDateRange = useCallback(
    (next: DateRange) => {
      setDateRangeState(next);
      writeDateRangeToURL(router, pathname, searchParams, next);
      setPage(1);
    },
    [router, pathname, searchParams],
  );

  const [formOpen, setFormOpen] = useState(false);
  const [editingItem, setEditingItem] = useState<T | null>(null);
  // Starting values for the next create. Used by "add a child here" in the tree
  // view, and by anything else that opens a form already scoped to a parent.
  const [formDefaults, setFormDefaults] = useState<Record<string, unknown> | undefined>(undefined);
  const [confirmOpen, setConfirmOpen] = useState(false);
  const [deletingId, setDeletingId] = useState<string | null>(null);
  const [bulkConfirmOpen, setBulkConfirmOpen] = useState(false);
  const [bulkArchiveOpen, setBulkArchiveOpen] = useState(false);
  const [bulkEditOpen, setBulkEditOpen] = useState(false);
  const [pendingCustom, setPendingCustom] = useState<CustomBulkAction<T> | null>(null);
  const [importOpen, setImportOpen] = useState(false);
  const tabs = useMemo(() => resource.table.tabs ?? [], [resource.table.tabs]);
  // First tab on load. A tab strip where nothing is selected reads as broken,
  // and the first tab is conventionally the unfiltered one.
  const [activeTab, setActiveTabState] = useState(() => tabs[0]?.key ?? "");
  const [showArchived, setShowArchivedState] = useState(false);
  const [liveMessage, setLiveMessage] = useState("");

  const queryClient = useQueryClient();

  // Mirrors the query useResource builds, so an export applies the same
  // filter and sort the operator is looking at.
  const apiSearchParams = useMemo(() => {
    const sp = new URLSearchParams();
    if (search) sp.set("search", search);
    if (sortBy) {
      sp.set("sort_by", sortBy);
      sp.set("sort_order", sortOrder);
    }
    Object.entries(filters).forEach(([k, v]) => {
      if (v) sp.set(k, v);
    });
    Object.entries(dateParams).forEach(([k, v]) => {
      if (v) sp.set(k, v);
    });
    const df = resource.table.dateFilter?.field;
    if (df && df !== "created_at") sp.set("date_field", df);
    return sp;
  }, [search, sortBy, sortOrder, filters, dateParams, resource.table.dateFilter?.field]);

  const { data, isLoading } = useResource<T>(resource.endpoint, {
    page,
    pageSize,
    search,
    sortBy,
    sortOrder,
    // Tab filters, then the operator's own, then the archived flag. The
    // operator's win: picking "Unpaid" and then filtering by customer should
    // narrow the tab, not silently leave it.
    filters: {
      ...(tabs.find((t) => t.key === activeTab)?.filters ?? {}),
      ...filters,
      ...(showArchived ? { archived: "true" } : {}),
    },
    dateParams,
    dateField: resource.table.dateFilter?.field,
  });

  const rows = useMemo(() => data?.data ?? [], [data]);

  // Switching views changes which rows exist, so a selection made in the
  // other one is stale. Keeping it is how you archive something you cannot
  // see.
  // Switching tabs changes which rows exist, so a selection made under the
  // other one is stale, the same reasoning as the archived view.
  const setActiveTab = useCallback((key: string) => {
    setActiveTabState(key);
    setSelection([]);
    setPage(1);
  }, []);

  const setShowArchived = useCallback((value: boolean) => {
    setShowArchivedState(value);
    setSelection([]);
    setPage(1);
  }, []);

  const singularName = resource.label?.singular ?? resource.name;
  const pluralName = resource.label?.plural ?? resource.slug;

  const { mutate: deleteItem, isPending: isDeleting } = useDeleteResource(
    resource.endpoint,
    singularName,
  );
  const { mutate: runBulk, isPending: isBulkPending } = useBulkResource(
    resource.endpoint,
    pluralName,
    singularName,
  );
  // Kept as its own name because the delete confirm dialog shows a spinner
  // for delete specifically, not for any bulk action in flight.
  const isBulkDeleting = isBulkPending;

  const columns = useMemo(
    () => resource.table.columns.filter((col) => !col.hidden && !hiddenColumns.includes(col.key)),
    [resource.table.columns, hiddenColumns],
  );

  const toggleColumn = useCallback((key: string) => {
    setHiddenColumns((prev) =>
      prev.includes(key) ? prev.filter((k) => k !== key) : [...prev, key],
    );
  }, []);

  // Any change to what is being queried resets to page 1 — otherwise a
  // search from page 7 lands on an empty page 7 of two results.
  const setSearch = useCallback((value: string) => {
    setSearchState(value);
    setPage(1);
  }, []);

  const setPageSize = useCallback((size: number) => {
    setPageSizeState(size);
    setPage(1);
  }, []);

  const setSort = useCallback(
    (key: string) => {
      if (sortBy === key) {
        setSortOrder((prev) => (prev === "asc" ? "desc" : "asc"));
      } else {
        setSortBy(key);
        setSortOrder("asc");
      }
      setPage(1);
    },
    [sortBy],
  );

  const setFilter = useCallback((key: string, value: string) => {
    setFilters((prev) => {
      if (!value) {
        const next = { ...prev };
        delete next[key];
        return next;
      }
      return { ...prev, [key]: value };
    });
    setPage(1);
  }, []);

  const clearSelection = useCallback(() => setSelection([]), []);

  const view = useCallback(
    (row: T) => {
      const id = String((row as Record<string, unknown>).id);
      router.push("/resources/" + resource.slug + "/" + id);
    },
    [router, resource.slug],
  );

  const edit = useCallback(
    (row: T) => {
      if (isFormPage) {
        const id = String((row as Record<string, unknown>).id);
        router.push("/resources/" + resource.slug + "?action=edit&edit=" + id);
      } else {
        setEditingItem(row);
        setFormOpen(true);
      }
    },
    [isFormPage, router, resource.slug],
  );

  const create = useCallback(() => {
    setFormDefaults(undefined);
    if (isFormPage) {
      router.push("/resources/" + resource.slug + "?action=create");
    } else {
      setEditingItem(null);
      setFormOpen(true);
    }
  }, [isFormPage, router, resource.slug]);

  /**
   * Create, with some fields already filled in.
   *
   * createWith({ parent_id: id }) is how the tree view adds a child to the row
   * you clicked. In page mode the values ride along as query params, because a
   * route change is the only state that survives the navigation.
   */
  const createWith = useCallback(
    (defaults: Record<string, unknown>) => {
      if (isFormPage) {
        const params = new URLSearchParams({ action: "create" });
        for (const [key, value] of Object.entries(defaults)) {
          if (value !== undefined && value !== null) params.set(key, String(value));
        }
        router.push("/resources/" + resource.slug + "?" + params.toString());
        return;
      }
      setFormDefaults(defaults);
      setEditingItem(null);
      setFormOpen(true);
    },
    [isFormPage, router, resource.slug],
  );

  const remove = useCallback((id: string) => {
    setDeletingId(id);
    setConfirmOpen(true);
  }, []);

  const doDelete = useCallback(() => {
    if (deletingId !== null) {
      deleteItem(deletingId, {
        onSuccess: () => {
          setConfirmOpen(false);
          setDeletingId(null);
        },
      });
    }
  }, [deleteItem, deletingId]);

  const bulkRemove = useCallback(() => {
    if (selection.length > 0) setBulkConfirmOpen(true);
  }, [selection]);

  const doBulkDelete = useCallback(() => {
    runBulk(
      { action: "delete", ids: selection },
      {
        onSuccess: () => {
          setBulkConfirmOpen(false);
          setSelection([]);
        },
      },
    );
  }, [runBulk, selection]);

  // ── the rest of the bulk surface ──────────────────────────────────────

  // The rows behind the selection. Custom actions get these so "email the
  // people I ticked" does not need a second round trip for data already here.
  const selectedRows = useMemo(
    () => rows.filter((row) => selection.includes(String((row as Record<string, unknown>).id))),
    [rows, selection],
  );

  const announce = useCallback((message: string) => {
    // Cleared first: setting the same string twice is not a change, and a
    // live region that has not changed says nothing. Two identical bulk
    // actions in a row would be announced once.
    setLiveMessage("");
    requestAnimationFrame(() => setLiveMessage(message));
  }, []);

  const refresh = useCallback(() => {
    queryClient.invalidateQueries({ queryKey: [resource.endpoint] });
  }, [queryClient, resource.endpoint]);

  const bulkArchive = useCallback(() => {
    if (selection.length > 0) setBulkArchiveOpen(true);
  }, [selection]);

  const doBulkArchive = useCallback(() => {
    runBulk(
      { action: "archive", ids: selection },
      {
        onSuccess: () => {
          setBulkArchiveOpen(false);
          setSelection([]);
          announce(selection.length + " archived.");
        },
      },
    );
  }, [runBulk, selection, announce]);

  // No confirm: putting something back is not destructive, and a dialog in
  // front of an undo is a dialog nobody reads.
  const bulkRestore = useCallback(() => {
    if (selection.length === 0) return;
    runBulk(
      { action: "restore", ids: selection },
      {
        onSuccess: () => {
          setSelection([]);
          announce(selection.length + " restored.");
        },
      },
    );
  }, [runBulk, selection, announce]);

  const bulkEdit = useCallback(() => {
    if (selection.length > 0) setBulkEditOpen(true);
  }, [selection]);

  const applyBulkEdit = useCallback(
    (patch: Record<string, unknown>) => {
      runBulk(
        { action: "patch", ids: selection, patch },
        {
          onSuccess: () => {
            setBulkEditOpen(false);
            setSelection([]);
            announce(selection.length + " updated.");
          },
        },
      );
    },
    [runBulk, selection, announce],
  );

  const runCustom = useCallback(
    (action: CustomBulkAction<T>) => {
      void action.onSelect(selection, selectedRows, {
        refresh,
        clearSelection: () => setSelection([]),
        announce,
      });
    },
    [selection, selectedRows, refresh, announce],
  );

  const runBulkAction = useCallback(
    (action: CustomBulkAction<T>) => {
      if (action.confirm) {
        setPendingCustom(action);
        return;
      }
      runCustom(action);
    },
    [runCustom],
  );

  // Restore only makes sense on rows that are archived, and archive only on
  // rows that are not, so the two never appear together. Offering both is how
  // an operator ends up archiving what they meant to bring back.
  const bulkActions = useMemo(() => {
    // ["edit", "export", "delete"] rather than ["delete"] alone: a resource
    // that predates bulkActions still gets the three that work against any
    // API. Archive and restore are opt-in because they need both the column
    // and the endpoint.
    const configured = resource.table.bulkActions ?? ["edit", "export", "delete"];
    return configured.filter((action) => {
      if (action === "restore") return showArchived;
      if (action === "archive") return !showArchived;
      return true;
    });
  }, [resource.table.bulkActions, showArchived]);

  const customBulkActions = useMemo(() => {
    const all = (resource.customBulkActions ?? []) as CustomBulkAction<T>[];
    return all.filter((action) => !action.visible || action.visible(selectedRows));
  }, [resource.customBulkActions, selectedRows]);

  const closeForm = useCallback(() => {
    setFormOpen(false);
    setEditingItem(null);
  }, []);

  const actions = resource.table.actions ?? ["create", "view", "edit", "delete"];
  const can = useCallback((action: TableAction) => actions.includes(action), [actions]);

  const statsConfig = resource.stats;
  const statsEnabled =
    statsConfig === undefined ||
    statsConfig === true ||
    (typeof statsConfig === "object" && statsConfig !== null && statsConfig.enabled !== false);

  const stats: StatCard[] | undefined = useMemo(() => {
    if (!statsEnabled) return undefined;

    // Every stat endpoint gets whatever narrows the table, or "Total: 10,000"
    // sits above a table showing 142 matches. The archived view counts as
    // narrowing: without it the Archived tab reads "Total 9" over two rows.
    const applyViewParams = (cards: StatCard[]): StatCard[] => {
      const extra: Record<string, string> = { ...dateParams };
      if (showArchived) extra.archived = "true";
      if (Object.keys(extra).length === 0) return cards;
      return cards.map((card) => {
        if (!card.endpoint) return card;
        const sep = card.endpoint.includes("?") ? "&" : "?";
        const qs = new URLSearchParams(extra).toString();
        return { ...card, endpoint: card.endpoint + sep + qs };
      });
    };

    if (
      typeof statsConfig === "object" &&
      statsConfig !== null &&
      Array.isArray(statsConfig.cards) &&
      statsConfig.cards.length > 0
    ) {
      return applyViewParams(statsConfig.cards);
    }

    const ep = resource.endpoint;
    const defaults: StatCard[] = [
      { label: "Total", endpoint: ep + "?page_size=1", field: "meta.total", icon: resource.icon || "Package" },
      { label: "This Week", endpoint: ep + "?page_size=1&created_since=7d", field: "meta.total", icon: "TrendingUp", color: "success" },
      { label: "This Month", endpoint: ep + "?page_size=1&created_since=30d", field: "meta.total", icon: "Calendar", color: "info" },
      { label: "Updated Recently", endpoint: ep + "?page_size=1&updated_since=7d", field: "meta.total", icon: "RefreshCw" },
    ];
    return applyViewParams(defaults);
  }, [statsEnabled, statsConfig, resource.endpoint, resource.icon, dateParams, showArchived]);

  return {
    resource,

    rows,
    meta: data?.meta,
    total: data?.meta?.total ?? 0,
    totalPages: data?.meta?.pages ?? 1,
    isLoading,

    page,
    pageSize,
    search,
    sortBy,
    sortOrder,
    filters,
    dateRange,
    setPage,
    setPageSize,
    setSearch,
    setSort,
    setFilter,
    setDateRange,

    columns,
    allColumns: resource.table.columns,
    hiddenColumns,
    toggleColumn,

    selection,
    setSelection,
    clearSelection,

    actions,
    can,
    create,
    createWith,
    edit,
    view,
    remove,
    bulkRemove,
    isDeleting,
    isBulkDeleting,

    bulkActions,
    customBulkActions,
    selectedRows,
    bulkArchive,
    bulkRestore,
    bulkEdit,
    applyBulkEdit,
    runBulkAction,
    isBulkPending,
    refresh,
    announce,
    liveMessage,

    tabs,
    activeTab,
    setActiveTab,

    showArchived,
    setShowArchived,

    form: { open: formOpen, item: editingItem, defaults: formDefaults, close: closeForm },
    confirmDelete: {
      open: confirmOpen,
      confirm: doDelete,
      cancel: () => {
        setConfirmOpen(false);
        setDeletingId(null);
      },
    },
    confirmBulkDelete: {
      open: bulkConfirmOpen,
      confirm: doBulkDelete,
      cancel: () => setBulkConfirmOpen(false),
    },
    confirmBulkArchive: {
      open: bulkArchiveOpen,
      confirm: doBulkArchive,
      cancel: () => setBulkArchiveOpen(false),
    },
    bulkEditor: {
      open: bulkEditOpen,
      close: () => setBulkEditOpen(false),
    },
    confirmCustom: {
      open: pendingCustom !== null,
      action: pendingCustom,
      confirm: () => {
        if (pendingCustom) runCustom(pendingCustom);
        setPendingCustom(null);
      },
      cancel: () => setPendingCustom(null),
    },
    importer: { open: importOpen, setOpen: setImportOpen },

    apiSearchParams,
    stats,
    singularName,
    pluralName,
    isFormPage,
    isSteps,
  };
}
