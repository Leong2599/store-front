package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"store-front/apps/api/internal/services"
	"store-front/apps/api/internal/sync"
)

// SyncHandler implements /api/sync/push and /api/sync/pull. The push
// endpoint applies a batch of client changes with per-change version
// checking; the pull endpoint streams server-side updates since a
// caller-supplied cursor.
type SyncHandler struct {
	DB       *gorm.DB
	Registry *sync.Registry
}

// NewSyncHandler wires the handler to the database + model registry.
func NewSyncHandler(db *gorm.DB, reg *sync.Registry) *SyncHandler {
	return &SyncHandler{DB: db, Registry: reg}
}

// PushChange is one entry in a /api/sync/push batch. Op is one of
// "create" / "update" / "delete". Version is the version the client
// believes the server has — mismatches surface as VERSION_CONFLICT.
type PushChange struct {
	Op      string                 `json:"op"`
	Model   string                 `json:"model"`
	ID      string                 `json:"id"`
	Version int                    `json:"version"`
	Data    map[string]interface{} `json:"data"`
}

// PushResult is the per-change result returned in the same order as
// the input batch. On VERSION_CONFLICT, ServerVersion + ServerData
// carry the current server state so the client can build a merge UI.
type PushResult struct {
	OK            bool        `json:"ok"`
	Code          string      `json:"code,omitempty"`
	Message       string      `json:"message,omitempty"`
	ServerVersion int         `json:"server_version,omitempty"`
	ServerData    interface{} `json:"server_data,omitempty"`
	NewVersion    int         `json:"new_version,omitempty"`
}

// Changes made while offline.
type SyncPushRequest struct {
	Changes []PushChange `json:"changes"`
}

// Push handles POST /api/sync/push. Each change is applied
// independently — one conflict does not abort the rest of the batch.

func (h *SyncHandler) Push(c *gin.Context) {
	var req SyncPushRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_BODY", "message": err.Error()}})
		return
	}

	results := make([]PushResult, len(req.Changes))
	for i, ch := range req.Changes {
		results[i] = h.applyChange(c, ch)
	}
	c.JSON(http.StatusOK, gin.H{"results": results})
}

// syncIdentifier picks a human-friendly label for the semantic activity feed
// from a change payload (name / title / slug / email), falling back to the id.
func syncIdentifier(data map[string]interface{}, id string) string {
	for _, k := range []string{"name", "title", "slug", "email"} {
		if v, ok := data[k]; ok {
			if s, ok := v.(string); ok && s != "" {
				return s
			}
		}
	}
	return id
}

func (h *SyncHandler) applyChange(c *gin.Context, ch PushChange) PushResult {
	proto, err := h.Registry.New(ch.Model)
	if err != nil {
		return PushResult{OK: false, Code: "UNKNOWN_MODEL", Message: err.Error()}
	}
	policy := h.Registry.PolicyFor(ch.Model)
	// Enforced before the payload reaches a decoder: a field declared
	// local_only is a promise it never leaves the device, and a promise kept
	// only by well-behaved clients is not one.
	ch.Data = policy.StripLocalOnly(ch.Data)
	// The Go struct name (e.g. "Category") is the nicest entity label for the
	// activity feed — offline edits should read the same as online ones.
	entityType := reflect.TypeOf(proto).Elem().Name()

	switch ch.Op {
	case "create":
		// Decode the client payload into a fresh model struct and insert.
		// We trust the client-supplied ID (UUID) so the local outbox can
		// keep referring to the same row after the server insert.
		obj := proto
		if err := decodeInto(obj, ch.Data); err != nil {
			return PushResult{OK: false, Code: "DECODE_ERROR", Message: err.Error()}
		}
		setField(obj, "ID", ch.ID)
		if err := h.DB.Create(obj).Error; err != nil {
			return PushResult{OK: false, Code: "CREATE_FAILED", Message: err.Error()}
		}
		// Mirror the online handler: emit a semantic activity row so offline
		// creates surface in /system/activity, not just the raw audit log.
		services.LogCreate(h.DB, c, entityType, syncIdentifier(ch.Data, ch.ID), ch.ID, "")
		return PushResult{OK: true, NewVersion: 1}

	case "update":
		// Versioned update: load current row, compare versions, update if match.
		current := proto
		if err := h.DB.First(current, "id = ?", ch.ID).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return PushResult{OK: false, Code: "NOT_FOUND", Message: "row was deleted on the server"}
			}
			return PushResult{OK: false, Code: "INTERNAL_ERROR", Message: err.Error()}
		}
		serverVersion := getIntField(current, "Version")
		if serverVersion != ch.Version {
			// The versions disagree. What happens next is the resource's
			// declared policy, decided here rather than in the client, because
			// a rule an old build can ignore is not a rule.
			switch policy.Conflict {
			case sync.ConflictServerWins:
				// The client's change is dropped and it is told to take the
				// server row. Reported distinctly from a conflict so the
				// client can apply it without asking anyone.
				return PushResult{
					OK:            false,
					Code:          "SERVER_WINS",
					Message:       fmt.Sprintf("server v%d kept over client v%d", serverVersion, ch.Version),
					ServerVersion: serverVersion,
					ServerData:    current,
				}
			case sync.ConflictClientWins:
				// Fall through and overwrite. The version check was protecting
				// nothing for this resource, and the author said so.
			default:
				return PushResult{
					OK:            false,
					Code:          "VERSION_CONFLICT",
					Message:       fmt.Sprintf("client had v%d, server has v%d", ch.Version, serverVersion),
					ServerVersion: serverVersion,
					ServerData:    current,
				}
			}
		}
		// Apply the update.
		//
		// Decode the client payload into a fresh, typed model struct rather than
		// calling .Updates(ch.Data) directly. A raw map[string]interface{} hands
		// nested values (a FileRef image, a FileRefs slice, a belongs-to relation
		// object) straight to the DB driver, which cannot encode a Go map into a
		// json column ("cannot find encode plan for OID 0") — the update fails and
		// the offline outbox entry gets stuck forever. Decoding first routes those
		// fields through their driver.Valuer implementations, exactly like create.
		obj := proto
		if err := decodeInto(obj, ch.Data); err != nil {
			return PushResult{OK: false, Code: "DECODE_ERROR", Message: err.Error()}
		}
		setField(obj, "ID", ch.ID)
		// Seed Version with the server's value so the BeforeUpdate hook bumps it to
		// serverVersion+1 regardless of what the client sent in the payload.
		setIntField(obj, "Version", serverVersion)
		// Save writes every column (so cleared/zeroed fields persist) and runs the
		// BeforeUpdate hook. Omit associations so the nested relation object is not
		// upserted, and CreatedAt so the client can't rewind the original timestamp.
		if err := h.DB.Omit(clause.Associations, "CreatedAt").Save(obj).Error; err != nil {
			return PushResult{OK: false, Code: "UPDATE_FAILED", Message: err.Error()}
		}
		newVersion := getIntField(obj, "Version")
		services.LogUpdate(h.DB, c, entityType, syncIdentifier(ch.Data, ch.ID), ch.ID, services.DiffSummary(ch.Data))
		return PushResult{OK: true, NewVersion: newVersion}

	case "delete":
		current := proto
		if err := h.DB.First(current, "id = ?", ch.ID).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				// Already gone — treat as success so the outbox can clear.
				return PushResult{OK: true}
			}
			return PushResult{OK: false, Code: "INTERNAL_ERROR", Message: err.Error()}
		}
		serverVersion := getIntField(current, "Version")
		if ch.Version != 0 && serverVersion != ch.Version {
			return PushResult{
				OK:            false,
				Code:          "VERSION_CONFLICT",
				Message:       "row was modified after the client's last sync",
				ServerVersion: serverVersion,
				ServerData:    current,
			}
		}
		if err := h.DB.Delete(current, "id = ?", ch.ID).Error; err != nil {
			return PushResult{OK: false, Code: "DELETE_FAILED", Message: err.Error()}
		}
		services.LogDelete(h.DB, c, entityType, ch.ID, ch.ID)
		return PushResult{OK: true}

	default:
		return PushResult{OK: false, Code: "INVALID_OP", Message: "op must be create, update, or delete"}
	}
}

// SyncPolicyResponse is what GET /api/sync/policy answers with.
type SyncPolicyResponse struct {
	Models map[string]sync.Policy `json:"models"`
}

// Policy handles GET /api/sync/policy.
//
// Clients configure themselves from this rather than from a generated copy.
// A copy is a second thing to keep in sync, and an offline client running
// last month's build against this month's conflict rules is exactly the
// silent failure this whole feature exists to prevent.
func (h *SyncHandler) Policy(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"data": SyncPolicyResponse{Models: h.Registry.Policies()}})
}

// Pull handles GET /api/sync/pull?since=<rfc3339>&model=<table>. Returns
// every row in the requested table with UpdatedAt > since. The client
// uses the response's cursor as the next ?since value.
func (h *SyncHandler) Pull(c *gin.Context) {
	model := c.Query("model")
	if model == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "MISSING_MODEL", "message": "?model is required"}})
		return
	}
	sinceStr := c.DefaultQuery("since", "")
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "500"))
	if limit < 1 || limit > 5000 {
		limit = 500
	}

	proto, err := h.Registry.New(model)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "UNKNOWN_MODEL", "message": err.Error()}})
		return
	}
	policy := h.Registry.PolicyFor(model)
	if policy.Mode == sync.ModeOnlineOnly {
		// Answering with an empty page would look like "nothing has changed",
		// and the client would mirror an empty table forever.
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
			"code":    "NOT_SYNCABLE",
			"message": model + " is registered online_only and is not mirrored",
		}})
		return
	}

	// Build a slice of the right type via reflection.
	sliceType := reflect.SliceOf(reflect.TypeOf(proto).Elem())
	results := reflect.New(sliceType)

	// Effective change time = the LATER of updated_at and deleted_at. A soft
	// delete only sets deleted_at, so ordering/cursoring on updated_at alone
	// would never carry the delete to offline clients (they'd keep a ghost
	// row forever). We order + cursor on the effective time and mark deleted
	// rows with "_deleted": true so the client can drop them from its mirror.
	effExpr := "MAX(updated_at, COALESCE(deleted_at, updated_at))"
	if h.DB.Dialector.Name() == "postgres" {
		effExpr = "GREATEST(updated_at, COALESCE(deleted_at, updated_at))"
	}

	// Unscoped so soft-deleted rows are included (they're the tombstones).
	q := h.DB.Unscoped().Model(proto)
	if sinceStr != "" {
		t, err := time.Parse(time.RFC3339Nano, sinceStr)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_SINCE", "message": err.Error()}})
			return
		}
		q = q.Where("updated_at > ? OR deleted_at > ?", t, t)
	}
	if err := q.Order(effExpr + " asc").Limit(limit).Find(results.Interface()).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "INTERNAL_ERROR", "message": err.Error()}})
		return
	}

	rs := results.Elem()
	rows := make([]map[string]interface{}, 0, rs.Len())
	cursor := sinceStr
	var maxEff time.Time
	for i := 0; i < rs.Len(); i++ {
		item := rs.Index(i).Addr().Interface()
		b, merr := json.Marshal(item)
		if merr != nil {
			continue
		}
		var m map[string]interface{}
		if uerr := json.Unmarshal(b, &m); uerr != nil {
			continue
		}
		m["_deleted"] = isSyncDeleted(item)
		rows = append(rows, policy.Projects(m))
		if eff, ok := effectiveSyncTime(item); ok && eff.After(maxEff) {
			maxEff = eff
		}
	}
	if !maxEff.IsZero() {
		cursor = maxEff.Format(time.RFC3339Nano)
	}

	c.JSON(http.StatusOK, gin.H{
		"data":   rows,
		"cursor": cursor,
		"count":  len(rows),
	})
}

// isSyncDeleted reports whether a model row is soft-deleted (a tombstone).
func isSyncDeleted(obj interface{}) bool {
	v := reflect.ValueOf(obj)
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}
	f := v.FieldByName("DeletedAt")
	if !f.IsValid() {
		return false
	}
	if d, ok := f.Interface().(gorm.DeletedAt); ok {
		return d.Valid
	}
	return false
}

// effectiveSyncTime returns the later of a row's UpdatedAt and DeletedAt — the
// timestamp the pull cursor advances on so both edits and deletes are carried.
func effectiveSyncTime(obj interface{}) (time.Time, bool) {
	v := reflect.ValueOf(obj)
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}
	var eff time.Time
	if f := v.FieldByName("UpdatedAt"); f.IsValid() {
		if t, ok := f.Interface().(time.Time); ok {
			eff = t
		}
	}
	if f := v.FieldByName("DeletedAt"); f.IsValid() {
		if d, ok := f.Interface().(gorm.DeletedAt); ok && d.Valid && d.Time.After(eff) {
			eff = d.Time
		}
	}
	return eff, !eff.IsZero()
}

// decodeInto round-trips a map through JSON into the target struct so
// gorm field tags + types are respected. Cheap; the maps are small.
func decodeInto(target interface{}, data map[string]interface{}) error {
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, target)
}

// setField sets a string field on a struct via reflection. Used for ID.
func setField(obj interface{}, name, value string) {
	v := reflect.ValueOf(obj).Elem()
	f := v.FieldByName(name)
	if f.IsValid() && f.CanSet() && f.Kind() == reflect.String {
		f.SetString(value)
	}
}

func getIntField(obj interface{}, name string) int {
	v := reflect.ValueOf(obj)
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}
	f := v.FieldByName(name)
	if !f.IsValid() {
		return 0
	}
	return int(f.Int())
}

// setIntField sets an int field on a struct via reflection. Used to seed
// Version before an update save so the BeforeUpdate hook bumps from the
// server's value rather than whatever the client happened to send.
func setIntField(obj interface{}, name string, value int) {
	v := reflect.ValueOf(obj).Elem()
	f := v.FieldByName(name)
	if f.IsValid() && f.CanSet() && f.CanInt() {
		f.SetInt(int64(value))
	}
}

func getTimeField(obj interface{}, name string) (time.Time, bool) {
	v := reflect.ValueOf(obj)
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}
	f := v.FieldByName(name)
	if !f.IsValid() {
		return time.Time{}, false
	}
	t, ok := f.Interface().(time.Time)
	return t, ok
}
