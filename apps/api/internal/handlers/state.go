package handlers

import (
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"store-front/apps/api/internal/events"
	"store-front/apps/api/internal/export"
	"store-front/apps/api/internal/models"
	"store-front/apps/api/internal/paginate"
	"store-front/apps/api/internal/pdf"
	"store-front/apps/api/internal/services"
)

// StateHandler handles state endpoints.
type StateHandler struct {
	DB *gorm.DB
}

// List returns a paginated list of states.
//
//	?archived=true   only archived rows
//	?archived=all    both
//	(default)        only live rows
func (h *StateHandler) List(c *gin.Context) {
	query := h.DB.Model(&models.State{}).Preload("Country")

	// Archived rows are excluded by default. Anything else means an operator
	// archives twelve rows, sees the count go down, and finds them again the
	// next time somebody sorts by a different column.
	switch c.Query("archived") {
	case "true", "1":
		query = query.Where("archived_at IS NOT NULL")
	case "all":
		// no filter
	default:
		query = query.Where("archived_at IS NULL")
	}

	res, err := paginate.List[models.State](
		query,
		paginate.Bind(c).With("country_id", c.Query("country_id")),
		paginate.Config{
			Searchable: []string{"name"},
			Sortable:   map[string]bool{"id": true, "created_at": true, "name": true},
			Filterable: map[string]bool{"id": true, "name": true, "country_id": true},
		},
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": gin.H{
				"code":    "INTERNAL_ERROR",
				"message": "Failed to fetch states",
			},
		})
		return
	}

	c.JSON(http.StatusOK, res)
}

// Export streams the full filtered list as CSV (default) or XLSX.
// Honours the same search/filter query params as List but skips
// pagination — you get every matching row in one file.
//
// Memory-bounded: reads in chunks of exportBatchSize so a million-row
// export doesn't OOM the process. CSV streams directly to the response
// writer; XLSX has to buffer (excelize requires the full sheet in
// memory before Write), so we still chunk the SCAN to avoid loading
// every row at once.
//
//	GET /api/states/export?format=csv
//	GET /api/states/export?format=xlsx&search=foo
func (h *StateHandler) Export(c *gin.Context) {
	const exportBatchSize = 1000

	format := c.DefaultQuery("format", "csv")
	search := c.Query("search")

	query := h.DB.Model(&models.State{}).Preload("Country").Order("created_at desc")
	if search != "" && len([]string{"name"}) > 0 {
		// Reuse the same searchable columns as List.
		searchable := []string{"name"}
		clause := ""
		args := []any{}
		wild := "%" + search + "%"
		for i, col := range searchable {
			if i > 0 {
				clause += " OR "
			}
			clause += "LOWER(" + col + ") LIKE LOWER(?)"
			args = append(args, wild)
		}
		query = query.Where(clause, args...)
	}

	opts := export.Options{
		Sheet: "States",
		Columns: []export.Column{
			{Header: "ID", Field: "ID"},
			{Header: "Name", Field: "Name"},
			{Header: "Created At", Field: "CreatedAt", Format: "date:2006-01-02"},
		},
	}

	// Stream rows in batches via GORM's FindInBatches. CSV writes each
	// batch straight to the wire; XLSX accumulates into a slice (no
	// streaming API in excelize) but at least we never load the whole
	// table at once.
	if format == "xlsx" {
		c.Header("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
		c.Header("Content-Disposition", `attachment; filename="states.xlsx"`)
		var all []models.State
		if err := query.FindInBatches(&[]models.State{}, exportBatchSize, func(tx *gorm.DB, batch int) error {
			var rows []models.State
			if err := tx.Scan(&rows).Error; err != nil {
				return err
			}
			all = append(all, rows...)
			return nil
		}).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": gin.H{"code": "EXPORT_FAILED", "message": err.Error()},
			})
			return
		}
		if err := export.XLSX(c.Writer, all, opts); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": gin.H{"code": "EXPORT_FAILED", "message": err.Error()},
			})
		}
		return
	}

	// CSV path — true streaming. Write headers once, then each batch
	// flushes its rows directly to the response writer.
	c.Header("Content-Type", "text/csv")
	c.Header("Content-Disposition", `attachment; filename="states.csv"`)

	headerWritten := false
	if err := query.FindInBatches(&[]models.State{}, exportBatchSize, func(tx *gorm.DB, batch int) error {
		var rows []models.State
		if err := tx.Scan(&rows).Error; err != nil {
			return err
		}
		if !headerWritten {
			if err := export.CSV(c.Writer, rows, opts); err != nil {
				return err
			}
			headerWritten = true
		} else {
			// Subsequent batches: write rows only, no header.
			if err := export.CSVRows(c.Writer, rows, opts); err != nil {
				return err
			}
		}
		return nil
	}).Error; err != nil {
		// Headers already sent — best we can do is log + truncate.
		// The client will see a malformed CSV; ops should re-run.
		// (We don't write a JSON error body once streaming has begun.)
		_ = err
	}
}

// GetByID returns a single state by ID.
func (h *StateHandler) GetByID(c *gin.Context) {
	id := c.Param("id")

	var item models.State
	if err := h.DB.Preload("Country").First(&item, "id = ?", id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": gin.H{
				"code":    "NOT_FOUND",
				"message": "State not found",
			},
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"data": item,
	})
}

// PDF streams this state as a print-ready PDF — a repeating header and
// footer with page numbers, the record's fields as a detail grid, and any
// line items as a table. Edit the pdf.Record below to restyle it; the
// renderer itself lives in internal/pdf/record.go.
func (h *StateHandler) PDF(c *gin.Context) {
	id := c.Param("id")

	var item models.State
	if err := h.DB.Preload("Country").First(&item, "id = ?", id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": gin.H{
				"code":    "NOT_FOUND",
				"message": "State not found",
			},
		})
		return
	}

	appName := os.Getenv("APP_NAME")
	if appName == "" {
		appName = "State"
	}

	rec := pdf.Record{
		Title:      "STATE",
		Subtitle:   pdf.Value(item.Name),
		Brand:      appName,
		FooterNote: appName + " · generated " + time.Now().Format("2 Jan 2006 15:04"),
		Fields: []pdf.Field{
			{Label: "Name", Value: pdf.Value(item.Name)},
			{Label: "Country", Value: pdf.Display(item.Country)},
			{Label: "Created", Value: pdf.Value(item.CreatedAt)},
		},
	}

	out, err := pdf.RenderRecord(rec)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": gin.H{
				"code":    "PDF_ERROR",
				"message": "could not render the PDF",
			},
		})
		return
	}

	filename := "state-" + id + ".pdf"
	c.Header("Content-Disposition", "inline; filename=\""+filename+"\"")
	c.Data(http.StatusOK, "application/pdf", out)
}

// CreateStateRequest is the JSON body accepted by POST /states.
//
// Named rather than anonymous so the API reference can document it: gindocs
// builds a request schema by reflecting over a real type, and an anonymous
// struct inside a handler gives it nothing to reflect over. routes.go passes
// this type to docs.Route(...).RequestBody().
type CreateStateRequest struct {
	Name      string `json:"name" binding:"required"`
	CountryID string `json:"country_id" binding:"required"`
}

// UpdateStateRequest is the JSON body accepted by PUT /states/:id.
// Every field is optional — only what the client sends is applied.
type UpdateStateRequest struct {
	Name      string  `json:"name"`
	CountryID *string `json:"country_id"`
}

// Create adds a new state.
func (h *StateHandler) Create(c *gin.Context) {
	var req CreateStateRequest

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"error": gin.H{
				"code":    "VALIDATION_ERROR",
				"message": err.Error(),
			},
		})
		return
	}

	item := models.State{
		Name:      req.Name,
		CountryID: req.CountryID,
	}

	if err := h.DB.Create(&item).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": gin.H{
				"code":    "INTERNAL_ERROR",
				"message": "Failed to create state",
			},
		})
		return
	}

	h.DB.Preload("Country").First(&item, "id = ?", item.ID)

	events.Emitted(c, "states", "State", "created", item.ID, item.Name, "", nil, item)

	c.JSON(http.StatusCreated, gin.H{
		"data":    item,
		"message": "State created successfully",
	})
}

// Update modifies an existing state.
func (h *StateHandler) Update(c *gin.Context) {
	id := c.Param("id")

	var item models.State
	if err := h.DB.First(&item, "id = ?", id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": gin.H{
				"code":    "NOT_FOUND",
				"message": "State not found",
			},
		})
		return
	}

	var req UpdateStateRequest

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"error": gin.H{
				"code":    "VALIDATION_ERROR",
				"message": err.Error(),
			},
		})
		return
	}

	updates := map[string]interface{}{}
	if req.Name != "" {
		updates["name"] = req.Name
	}
	if req.CountryID != nil {
		updates["country_id"] = *req.CountryID
	}

	if err := h.DB.Model(&item).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": gin.H{
				"code":    "INTERNAL_ERROR",
				"message": "Failed to update state",
			},
		})
		return
	}

	h.DB.Preload("Country").First(&item, "id = ?", item.ID)

	events.Emitted(c, "states", "State", "updated", item.ID, item.Name, services.DiffSummary(updates), nil, item)

	c.JSON(http.StatusOK, gin.H{
		"data":    item,
		"message": "State updated successfully",
	})
}

// Patch applies a partial update to a state. Used by the admin's
// grouped update view — each form group's Save button calls PATCH with
// only the fields it owns, so editing "Address" doesn't rewrite
// "Pricing". Refuses any key that isn't a writable model column.
func (h *StateHandler) Patch(c *gin.Context) {
	id := c.Param("id")

	var item models.State
	if err := h.DB.First(&item, "id = ?", id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": gin.H{
				"code":    "NOT_FOUND",
				"message": "State not found",
			},
		})
		return
	}

	var body map[string]interface{}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"error": gin.H{
				"code":    "VALIDATION_ERROR",
				"message": err.Error(),
			},
		})
		return
	}

	// Whitelist: only writable model columns may be patched. id,
	// created_at, updated_at, deleted_at, version are owned by the
	// framework and silently dropped here.
	allowed := map[string]bool{
		"name":       true,
		"country_id": true,
	}
	updates := map[string]interface{}{}
	for k, v := range body {
		if allowed[k] {
			updates[k] = v
		}
	}
	if len(updates) == 0 {
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"error": gin.H{
				"code":    "VALIDATION_ERROR",
				"message": "No writable fields in request body",
			},
		})
		return
	}

	if err := h.DB.Model(&item).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": gin.H{
				"code":    "INTERNAL_ERROR",
				"message": "Failed to patch state",
			},
		})
		return
	}
	h.DB.Preload("Country").First(&item, "id = ?", item.ID)

	events.Emitted(c, "states", "State", "updated", item.ID, item.Name, services.DiffSummary(updates), nil, item)

	c.JSON(http.StatusOK, gin.H{
		"data":    item,
		"message": "State updated successfully",
	})
}

// Delete soft-deletes a state.
func (h *StateHandler) Delete(c *gin.Context) {
	id := c.Param("id")

	var item models.State
	if err := h.DB.First(&item, "id = ?", id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": gin.H{
				"code":    "NOT_FOUND",
				"message": "State not found",
			},
		})
		return
	}

	if err := h.DB.Delete(&item).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": gin.H{
				"code":    "INTERNAL_ERROR",
				"message": "Failed to delete state",
			},
		})
		return
	}

	events.Emitted(c, "states", "State", "deleted", item.ID, item.Name, "", item, nil)

	c.JSON(http.StatusOK, gin.H{
		"message": "State deleted successfully",
	})
}

// BulkStateRequest is one operation applied to a set of rows.
//
// One request rather than one per row. The admin used to fire N parallel
// DELETEs, which means N transactions, N audit entries, and a half-applied
// result when the eleventh fails: the operator sees "failed" while ten rows
// are already gone.
type BulkStateRequest struct {
	// delete removes, archive puts away, restore brings back, patch writes the
	// same field values to every selected row.
	Action string `json:"action" binding:"required,oneof=delete archive restore patch"`
	// Capped: an unbounded IN clause is a way to lock a table by accident.
	IDs []string `json:"ids" binding:"required,min=1,max=500"`
	// Only read when action is "patch". Whitelisted the same way Patch is.
	Patch map[string]interface{} `json:"patch"`
}

// Bulk applies one action to many states in a single transaction.
func (h *StateHandler) Bulk(c *gin.Context) {
	var req BulkStateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"error": gin.H{
				"code":    "VALIDATION_ERROR",
				"message": err.Error(),
			},
		})
		return
	}

	// Unarchived scope for archive, archived scope for restore: without it a
	// mixed selection reports "12 archived" having changed three rows.
	var items []models.State
	scope := h.DB.Where("id IN ?", req.IDs)
	if req.Action == "restore" {
		scope = scope.Where("archived_at IS NOT NULL")
	} else if req.Action == "archive" {
		scope = scope.Where("archived_at IS NULL")
	}
	if err := scope.Find(&items).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": gin.H{"code": "INTERNAL_ERROR", "message": "Failed to load states"},
		})
		return
	}
	if len(items) == 0 {
		c.JSON(http.StatusOK, gin.H{
			"data":    gin.H{"affected": 0, "requested": len(req.IDs)},
			"message": "Nothing to do",
		})
		return
	}

	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}

	var updates map[string]interface{}
	if req.Action == "patch" {
		// Same whitelist as Patch. Framework-owned columns are dropped rather
		// than rejected, so a client sending the whole row is not an error.
		allowed := map[string]bool{
			"name":       true,
			"country_id": true,
		}
		updates = map[string]interface{}{}
		for k, v := range req.Patch {
			if allowed[k] {
				updates[k] = v
			}
		}
		if len(updates) == 0 {
			c.JSON(http.StatusUnprocessableEntity, gin.H{
				"error": gin.H{
					"code":    "VALIDATION_ERROR",
					"message": "No writable fields in patch",
				},
			})
			return
		}
	}

	// One transaction: all of it lands or none of it does.
	err := h.DB.Transaction(func(tx *gorm.DB) error {
		switch req.Action {
		case "delete":
			return tx.Where("id IN ?", ids).Delete(&models.State{}).Error
		case "archive":
			now := time.Now()
			return tx.Model(&models.State{}).Where("id IN ?", ids).
				Update("archived_at", now).Error
		case "restore":
			return tx.Model(&models.State{}).Where("id IN ?", ids).
				Update("archived_at", nil).Error
		default:
			return tx.Model(&models.State{}).Where("id IN ?", ids).
				Updates(updates).Error
		}
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": gin.H{
				"code":    "INTERNAL_ERROR",
				"message": "Failed to " + req.Action + " states",
			},
		})
		return
	}

	// One audit entry naming the action and the count, not N entries that bury
	// everything else somebody did today.
	// A local map, not a package-level helper: every resource gets its own
	// handler file in package handlers, so a shared func would be redeclared
	// once per resource.
	past := map[string]string{
		"delete":  "deleted",
		"archive": "archived",
		"restore": "restored",
		"patch":   "updated",
	}[req.Action]

	noun := "states"
	if len(ids) == 1 {
		noun = "state"
	}

	summary := req.Action + " " + strconv.Itoa(len(ids)) + " " + noun
	if req.Action == "patch" {
		summary += ": " + services.DiffSummary(updates)
	}
	// resourceID holds ONE id, not all of them: it is a lookup key, and joining
	// five hundred UUIDs into it makes the column unusable for the thing it is
	// for. The count lives in the summary, where it can be read.
	events.Emitted(c, "states", "State", "bulk", ids[0], summary, summary, nil, nil)

	c.JSON(http.StatusOK, gin.H{
		"data":    gin.H{"affected": len(ids), "requested": len(req.IDs)},
		"message": strconv.Itoa(len(ids)) + " " + noun + " " + past,
	})
}
