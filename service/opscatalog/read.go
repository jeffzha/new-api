// Package opscatalog reads the models on sale and the distributor discounts from TokenOps
// (station spec v1 section 15, common rules 15.1) and keeps the last valid copy of each in
// ops_catalog_snapshots. It is off unless OPS_CATALOG_SYNC_ENABLED=true.
//
// The copies are only stored and shown; nothing here changes model visibility, group ratios or
// user prices. Reads never block relaying: one instance holds each row's lease and reads on its
// own schedule, a failed read keeps the previous copy, and a key without the scope (403
// scope_required) is retried quietly every few minutes.
package opscatalog

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/opspush"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	KindModels    = "models"
	KindDiscounts = "discounts"
	ModelsPath    = "/api/v1/catalog/v1/models"
	DiscountsPath = "/api/v1/catalog/v1/discounts"
	maxBody       = 5 * 1024 * 1024 // OPS caps a read at 4 MB (502 response_too_large)
	leaseSeconds  = 90
	tick          = 15 * time.Second
	scopeWait     = 10 * time.Minute
)

// kinds lists each read with its spec 15.2 / 15.3 interval.
var kinds = []struct {
	Kind, Path string
	Every      time.Duration
}{
	{KindModels, ModelsPath, 10 * time.Minute},
	{KindDiscounts, DiscountsPath, time.Minute},
}

// Snapshot is the last valid copy of one read and the state of its schedule.
type Snapshot struct {
	Kind        string `json:"kind" gorm:"primaryKey;type:varchar(16)"`
	Version     string `json:"version" gorm:"type:varchar(128);not null;default:''"`
	GeneratedAt string `json:"generated_at" gorm:"type:varchar(40);not null;default:''"`
	Items       int    `json:"items" gorm:"not null;default:0"`
	Body        string `json:"-" gorm:"type:text"`
	FetchedAt   int64  `json:"fetched_at" gorm:"not null;default:0"`
	CheckedAt   int64  `json:"checked_at" gorm:"not null;default:0"`
	NextAt      int64  `json:"next_at" gorm:"not null;default:0"`
	Attempts    int    `json:"attempts" gorm:"not null;default:0"`
	LastStatus  int    `json:"last_status" gorm:"not null;default:0"`
	LastError   string `json:"last_error" gorm:"type:varchar(255);not null;default:''"`
	Owner       string `json:"-" gorm:"type:varchar(64);not null;default:''"`
	LeaseUntil  int64  `json:"-" gorm:"not null;default:0"`
}

func (Snapshot) TableName() string { return "ops_catalog_snapshots" }

type Config struct{ URL, KeyID, Secret string }

type Reader struct {
	DB     *gorm.DB
	Config Config
	Client *http.Client
	Owner  string
	Now    func() time.Time
}

// StartConfigured starts the reader when OPS_CATALOG_SYNC_ENABLED=true.
func StartConfigured(ctx context.Context) error {
	if os.Getenv("OPS_CATALOG_SYNC_ENABLED") != "true" {
		return nil
	}
	config := Config{URL: strings.TrimRight(os.Getenv("OPS_PUSH_URL"), "/"), KeyID: os.Getenv("OPS_PUSH_KEY_ID"), Secret: os.Getenv("OPS_PUSH_SECRET")}
	if err := (opspush.Config{URL: config.URL, KeyID: config.KeyID, Secret: config.Secret}).Validate(); err != nil {
		return errors.New("OPS_CATALOG_SYNC_ENABLED needs a valid Ops push configuration: " + err.Error())
	}
	reader := New(model.DB, config)
	if err := reader.Migrate(); err != nil {
		return err
	}
	go reader.Run(ctx)
	common.SysLog("Ops catalog read enabled: " + config.URL + ModelsPath + ", " + DiscountsPath)
	return nil
}

func New(db *gorm.DB, config Config) *Reader {
	pusher := opspush.New(db, nil, opspush.Config{})
	return &Reader{DB: db, Config: config, Client: pusher.Client, Owner: pusher.Owner, Now: time.Now}
}

func (r *Reader) Migrate() error {
	if err := r.DB.AutoMigrate(&Snapshot{}); err != nil {
		return err
	}
	if common.UsingMainDatabase(common.DatabaseTypeMySQL) {
		// A copy can be up to 4 MB; MySQL TEXT stops at 64 KB.
		if err := r.DB.Exec("ALTER TABLE ops_catalog_snapshots MODIFY body MEDIUMTEXT").Error; err != nil {
			return err
		}
	}
	for _, kind := range kinds {
		if err := r.DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&Snapshot{Kind: kind.Kind}).Error; err != nil {
			return err
		}
	}
	return nil
}

func (r *Reader) Run(ctx context.Context) {
	for {
		r.Step(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(tick):
		}
	}
}

// Step reads every kind that is due and whose lease this instance holds.
func (r *Reader) Step(ctx context.Context) {
	for _, kind := range kinds {
		snapshot, ok := r.lease(kind.Kind)
		if ok {
			r.read(ctx, snapshot, kind.Path, kind.Every)
		}
	}
}

func (r *Reader) lease(kind string) (*Snapshot, bool) {
	now := r.Now().Unix()
	err := r.DB.Model(&Snapshot{}).Where("kind = ? AND next_at <= ? AND (owner = ? OR lease_until < ?)", kind, now, r.Owner, now).
		Updates(map[string]any{"owner": r.Owner, "lease_until": now + leaseSeconds}).Error
	// Read the owner back: MySQL counts changed rows, so RowsAffected is 0 when the lease is renewed within a second.
	var snapshot Snapshot
	if err != nil || r.DB.Where("kind = ?", kind).First(&snapshot).Error != nil || snapshot.Owner != r.Owner || snapshot.NextAt > now {
		return nil, false
	}
	return &snapshot, true
}

func (r *Reader) save(kind string, values map[string]any) {
	if err := r.DB.Model(&Snapshot{}).Where("kind = ? AND owner = ?", kind, r.Owner).Updates(values).Error; err != nil {
		common.SysError("Ops catalog " + kind + ": save failed: " + err.Error())
	}
}

func (r *Reader) read(ctx context.Context, snapshot *Snapshot, path string, every time.Duration) {
	now := r.Now()
	target := path
	if snapshot.Version != "" {
		target += "?since=" + url.QueryEscape(snapshot.Version)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, r.Config.URL+target, nil)
	if err != nil {
		r.failed(snapshot, 0, "request: "+err.Error(), every, 0)
		return
	}
	for name, values := range opspush.Sign(r.Config.KeyID, r.Config.Secret, http.MethodGet, target, nil, now) {
		request.Header[name] = values
	}
	if snapshot.Version != "" {
		request.Header.Set("If-None-Match", `"`+snapshot.Version+`"`)
	}
	response, err := r.Client.Do(request)
	if err != nil {
		r.failed(snapshot, 0, "unavailable", every, 0)
		return
	}
	raw, readErr := io.ReadAll(io.LimitReader(response.Body, maxBody+1))
	response.Body.Close()
	switch response.StatusCode {
	case http.StatusNotModified:
		r.save(snapshot.Kind, map[string]any{"checked_at": now.Unix(), "next_at": now.Add(every).Unix(), "attempts": 0,
			"last_status": http.StatusNotModified, "last_error": ""})
		return
	case http.StatusOK:
	default:
		var failure struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = common.Unmarshal(raw, &failure)
		code := failure.Error.Code
		if code == "" {
			code = http.StatusText(response.StatusCode)
		}
		retry, _ := strconv.Atoi(response.Header.Get("Retry-After"))
		r.failed(snapshot, response.StatusCode, code, every, time.Duration(retry)*time.Second)
		return
	}
	if readErr != nil || len(raw) > maxBody {
		r.failed(snapshot, response.StatusCode, "response unreadable or too large", every, 0)
		return
	}
	data, version, generatedAt, items, err := validate(snapshot.Kind, raw)
	if err == nil {
		if etag := strings.TrimPrefix(response.Header.Get("ETag"), "W/"); etag != "" && etag != `"`+version+`"` {
			err = errors.New("ETag does not match version")
		}
	}
	if err != nil {
		r.failed(snapshot, response.StatusCode, "invalid "+snapshot.Kind+": "+err.Error(), every, 0)
		return
	}
	if version != snapshot.Version {
		common.SysLog(fmt.Sprintf("Ops catalog %s: version %s (%d items, generated %s)", snapshot.Kind, version, items, generatedAt))
	}
	r.save(snapshot.Kind, map[string]any{"version": version, "generated_at": generatedAt, "items": items, "body": data,
		"fetched_at": now.Unix(), "checked_at": now.Unix(), "next_at": now.Add(every).Unix(), "attempts": 0,
		"last_status": http.StatusOK, "last_error": ""})
}

// failed keeps the previous copy and schedules the next read. A missing scope waits quietly; any error is logged
// only when it differs from the previous one, so a long outage does not flood the log.
func (r *Reader) failed(snapshot *Snapshot, status int, message string, every, retryAfter time.Duration) {
	if len(message) > 255 {
		message = message[:255]
	}
	attempts := snapshot.Attempts + 1
	wait := max(every, opspush.Backoff(attempts, retryAfter))
	if status == http.StatusForbidden && message == "scope_required" {
		wait = max(scopeWait, retryAfter)
	}
	if message != snapshot.LastError {
		text := fmt.Sprintf("Ops catalog %s: HTTP %d %s; keeping version %q, next read in %s", snapshot.Kind, status, message, snapshot.Version, wait.Round(time.Second))
		if status == http.StatusForbidden && message == "scope_required" {
			common.SysLog(text)
		} else {
			common.SysError(text)
		}
	}
	r.save(snapshot.Kind, map[string]any{"attempts": attempts, "last_status": status, "last_error": message,
		"next_at": r.Now().Add(wait).Unix()})
}

// validate checks a 200 body against spec 15.2 / 15.3 and returns the data object as stored.
func validate(kind string, raw []byte) (string, string, string, int, error) {
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := common.Unmarshal(raw, &envelope); err != nil || envelope.Data == nil {
		return "", "", "", 0, errors.New("not a data object")
	}
	data := envelope.Data
	version, _ := data["version"].(string)
	if version == "" || len(version) > 128 || strings.ContainsFunc(version, func(r rune) bool { return r > unicode.MaxASCII || !unicode.IsPrint(r) || r == '"' }) {
		return "", "", "", 0, errors.New("version")
	}
	generatedAt, _ := data["generatedAt"].(string)
	if _, err := time.Parse(time.RFC3339, generatedAt); err != nil {
		return "", "", "", 0, errors.New("generatedAt")
	}
	count := 0
	switch kind {
	case KindModels:
		items, ok := data["items"].([]any)
		if !ok {
			return "", "", "", 0, errors.New("items")
		}
		for index, value := range items {
			item, ok := value.(map[string]any)
			name, _ := item["model"].(string)
			_, callType := item["callType"].(string)
			_, onSale := item["onSale"].(bool)
			_, status := item["status"].(string)
			if !ok || name == "" || !callType || !onSale || !status {
				return "", "", "", 0, fmt.Errorf("items[%d]", index)
			}
		}
		count = len(items)
	case KindDiscounts:
		distributors, ok := data["distributors"].([]any)
		users, ok2 := data["users"].([]any)
		if !ok || !ok2 {
			return "", "", "", 0, errors.New("distributors or users")
		}
		for index, value := range distributors {
			item, ok := value.(map[string]any)
			id, _ := item["distributorId"].(string)
			_, models := item["models"].([]any)
			if !ok || id == "" || !models {
				return "", "", "", 0, fmt.Errorf("distributors[%d]", index)
			}
		}
		for index, value := range users {
			item, ok := value.(map[string]any)
			_, models := item["models"].([]any)
			if !ok || item["userId"] == nil || !models {
				return "", "", "", 0, fmt.Errorf("users[%d]", index)
			}
		}
		count = len(distributors) + len(users)
	}
	body, err := common.Marshal(data)
	if err != nil {
		return "", "", "", 0, err
	}
	return string(body), version, generatedAt, count, nil
}

// Current returns the stored state of every read (without the copies themselves), for the admin status endpoint.
func Current(db *gorm.DB) ([]Snapshot, error) {
	var rows []Snapshot
	if !db.Migrator().HasTable(&Snapshot{}) {
		return rows, nil
	}
	err := db.Omit("body").Order("kind").Find(&rows).Error
	return rows, err
}
