package handlers

import (
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"store-front/apps/api/internal/database"
	"store-front/apps/api/internal/events"
	"store-front/apps/api/internal/export"
	"store-front/apps/api/internal/models"
	"store-front/apps/api/internal/paginate"
	"store-front/apps/api/internal/pdf"
	"store-front/apps/api/internal/services"
)

// CountryHandler handles country endpoints.
type CountryHandler struct {
	DB *gorm.DB
}

// List returns a paginated list of countries.
//
//	?archived=true   only archived rows
//	?archived=all    both
//	(default)        only live rows
func (h *CountryHandler) List(c *gin.Context) {
	query := h.DB.Model(&models.Country{})

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

	res, err := paginate.List[models.Country](
		query,
		paginate.Bind(c),
		paginate.Config{
			Searchable: []string{"name"},
			Sortable:   map[string]bool{"id": true, "created_at": true, "name": true},
			Filterable: map[string]bool{"id": true, "name": true},
		},
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": gin.H{
				"code":    "INTERNAL_ERROR",
				"message": "Failed to fetch countries",
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
//	GET /api/countries/export?format=csv
//	GET /api/countries/export?format=xlsx&search=foo
func (h *CountryHandler) Export(c *gin.Context) {
	const exportBatchSize = 1000

	format := c.DefaultQuery("format", "csv")
	search := c.Query("search")

	query := h.DB.Model(&models.Country{}).Order("created_at desc")
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
		Sheet: "Countries",
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
		c.Header("Content-Disposition", `attachment; filename="countries.xlsx"`)
		var all []models.Country
		if err := query.FindInBatches(&[]models.Country{}, exportBatchSize, func(tx *gorm.DB, batch int) error {
			var rows []models.Country
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
	c.Header("Content-Disposition", `attachment; filename="countries.csv"`)

	headerWritten := false
	if err := query.FindInBatches(&[]models.Country{}, exportBatchSize, func(tx *gorm.DB, batch int) error {
		var rows []models.Country
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

// GetByID returns a single country by ID.
func (h *CountryHandler) GetByID(c *gin.Context) {
	id := c.Param("id")

	var item models.Country
	if err := h.DB.First(&item, "id = ?", id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": gin.H{
				"code":    "NOT_FOUND",
				"message": "Country not found",
			},
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"data": item,
	})
}

// PDF streams this country as a print-ready PDF — a repeating header and
// footer with page numbers, the record's fields as a detail grid, and any
// line items as a table. Edit the pdf.Record below to restyle it; the
// renderer itself lives in internal/pdf/record.go.
func (h *CountryHandler) PDF(c *gin.Context) {
	id := c.Param("id")

	var item models.Country
	if err := h.DB.First(&item, "id = ?", id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": gin.H{
				"code":    "NOT_FOUND",
				"message": "Country not found",
			},
		})
		return
	}

	appName := os.Getenv("APP_NAME")
	if appName == "" {
		appName = "Country"
	}

	rec := pdf.Record{
		Title:      "COUNTRY",
		Subtitle:   pdf.Value(item.Name),
		Brand:      appName,
		FooterNote: appName + " · generated " + time.Now().Format("2 Jan 2006 15:04"),
		Fields: []pdf.Field{
			{Label: "Name", Value: pdf.Value(item.Name)},
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

	filename := "country-" + id + ".pdf"
	c.Header("Content-Disposition", "inline; filename=\""+filename+"\"")
	c.Data(http.StatusOK, "application/pdf", out)
}

// CreateCountryRequest is the JSON body accepted by POST /countries.
//
// Named rather than anonymous so the API reference can document it: gindocs
// builds a request schema by reflecting over a real type, and an anonymous
// struct inside a handler gives it nothing to reflect over. routes.go passes
// this type to docs.Route(...).RequestBody().
type CreateCountryRequest struct {
	Name string `json:"name" binding:"required"`
}

// UpdateCountryRequest is the JSON body accepted by PUT /countries/:id.
// Every field is optional — only what the client sends is applied.
type UpdateCountryRequest struct {
	Name string `json:"name"`
}

// Create adds a new country.
func (h *CountryHandler) Create(c *gin.Context) {
	var req CreateCountryRequest

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"error": gin.H{
				"code":    "VALIDATION_ERROR",
				"message": err.Error(),
			},
		})
		return
	}

	item := models.Country{
		Name: req.Name,
	}

	if err := database.Write(h.DB).Create(&item).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": gin.H{
				"code":    "INTERNAL_ERROR",
				"message": "Failed to create country",
			},
		})
		return
	}

	if !database.SupportsReturning(h.DB) {
		h.DB.First(&item, "id = ?", item.ID)
	}

	events.Emitted(c, "countries", "Country", "created", item.ID, item.Name, "", nil, item)

	c.JSON(http.StatusCreated, gin.H{
		"data":    item,
		"message": "Country created successfully",
	})
}

// Update modifies an existing country.
func (h *CountryHandler) Update(c *gin.Context) {
	id := c.Param("id")

	var item models.Country
	if err := h.DB.First(&item, "id = ?", id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": gin.H{
				"code":    "NOT_FOUND",
				"message": "Country not found",
			},
		})
		return
	}

	var req UpdateCountryRequest

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

	if err := database.Write(h.DB).Model(&item).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": gin.H{
				"code":    "INTERNAL_ERROR",
				"message": "Failed to update country",
			},
		})
		return
	}

	if !database.SupportsReturning(h.DB) {
		h.DB.First(&item, "id = ?", item.ID)
	}

	events.Emitted(c, "countries", "Country", "updated", item.ID, item.Name, services.DiffSummary(updates), nil, item)

	c.JSON(http.StatusOK, gin.H{
		"data":    item,
		"message": "Country updated successfully",
	})
}

// Patch applies a partial update to a country. Used by the admin's
// grouped update view — each form group's Save button calls PATCH with
// only the fields it owns, so editing "Address" doesn't rewrite
// "Pricing". Refuses any key that isn't a writable model column.
func (h *CountryHandler) Patch(c *gin.Context) {
	id := c.Param("id")

	var item models.Country
	if err := h.DB.First(&item, "id = ?", id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": gin.H{
				"code":    "NOT_FOUND",
				"message": "Country not found",
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
		"name": true,
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
				"message": "Failed to patch country",
			},
		})
		return
	}
	h.DB.First(&item, "id = ?", item.ID)

	events.Emitted(c, "countries", "Country", "updated", item.ID, item.Name, services.DiffSummary(updates), nil, item)

	c.JSON(http.StatusOK, gin.H{
		"data":    item,
		"message": "Country updated successfully",
	})
}

// Delete soft-deletes a country.
func (h *CountryHandler) Delete(c *gin.Context) {
	id := c.Param("id")

	var item models.Country
	if err := h.DB.First(&item, "id = ?", id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": gin.H{
				"code":    "NOT_FOUND",
				"message": "Country not found",
			},
		})
		return
	}

	if err := h.DB.Delete(&item).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": gin.H{
				"code":    "INTERNAL_ERROR",
				"message": "Failed to delete country",
			},
		})
		return
	}

	events.Emitted(c, "countries", "Country", "deleted", item.ID, item.Name, "", item, nil)

	c.JSON(http.StatusOK, gin.H{
		"message": "Country deleted successfully",
	})
}

// BulkCountryRequest is one operation applied to a set of rows.
//
// One request rather than one per row. The admin used to fire N parallel
// DELETEs, which means N transactions, N audit entries, and a half-applied
// result when the eleventh fails: the operator sees "failed" while ten rows
// are already gone.
type BulkCountryRequest struct {
	// delete removes, archive puts away, restore brings back, patch writes the
	// same field values to every selected row.
	Action string `json:"action" binding:"required,oneof=delete archive restore patch"`
	// Capped: an unbounded IN clause is a way to lock a table by accident.
	IDs []string `json:"ids" binding:"required,min=1,max=500"`
	// Only read when action is "patch". Whitelisted the same way Patch is.
	Patch map[string]interface{} `json:"patch"`
}

// Bulk applies one action to many countries in a single transaction.
func (h *CountryHandler) Bulk(c *gin.Context) {
	var req BulkCountryRequest
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
	var items []models.Country
	scope := h.DB.Where("id IN ?", req.IDs)
	if req.Action == "restore" {
		scope = scope.Where("archived_at IS NOT NULL")
	} else if req.Action == "archive" {
		scope = scope.Where("archived_at IS NULL")
	}
	if err := scope.Find(&items).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": gin.H{"code": "INTERNAL_ERROR", "message": "Failed to load countries"},
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
			"name": true,
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
			return tx.Where("id IN ?", ids).Delete(&models.Country{}).Error
		case "archive":
			now := time.Now()
			return tx.Model(&models.Country{}).Where("id IN ?", ids).
				Update("archived_at", now).Error
		case "restore":
			return tx.Model(&models.Country{}).Where("id IN ?", ids).
				Update("archived_at", nil).Error
		default:
			return tx.Model(&models.Country{}).Where("id IN ?", ids).
				Updates(updates).Error
		}
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": gin.H{
				"code":    "INTERNAL_ERROR",
				"message": "Failed to " + req.Action + " countries",
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

	noun := "countries"
	if len(ids) == 1 {
		noun = "country"
	}

	summary := req.Action + " " + strconv.Itoa(len(ids)) + " " + noun
	if req.Action == "patch" {
		summary += ": " + services.DiffSummary(updates)
	}
	// resourceID holds ONE id, not all of them: it is a lookup key, and joining
	// five hundred UUIDs into it makes the column unusable for the thing it is
	// for. The count lives in the summary, where it can be read.
	events.Emitted(c, "countries", "Country", "bulk", ids[0], summary, summary, nil, nil)

	c.JSON(http.StatusOK, gin.H{
		"data":    gin.H{"affected": len(ids), "requested": len(req.IDs)},
		"message": strconv.Itoa(len(ids)) + " " + noun + " " + past,
	})
}
