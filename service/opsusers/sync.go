// Package opsusers pushes user profiles to TokenOps under station spec v1 section 11
// (POST /api/v1/ingest/v1/users). It is off unless OPS_USER_SYNC_ENABLED=true.
//
// The users table is scanned in id order and each profile is compared with the last
// one OPS answered 200 for (ops_user_profiles). A changed, new or removed user goes
// into the next batch; the first pass after enabling therefore pushes every user
// (spec 5.5), later passes only the changes (spec 11.3). One instance holds the lease
// (spec 4.1); a batch is persisted before it is sent and resent byte for byte until
// OPS answers, so a retry carries the same batchId and users.
package opsusers

import (
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/opspush"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	Path         = "/api/v1/ingest/v1/users"
	maxUsers     = 500
	maxBody      = 1024 * 1024
	maxText      = 200
	scanPage     = 1000
	scanPerStep  = 5000
	leaseSeconds = 60
	busyWait     = 3 * time.Second
	idleWait     = 2 * time.Minute
	stalledWait  = 10 * time.Minute
)

// State is the single pusher row (id 1).
type State struct {
	Id            int    `gorm:"primaryKey;autoIncrement:false"`
	ScanAfter     int64  `gorm:"not null;default:0"`
	Resend        string `gorm:"type:varchar(64);not null;default:''"`
	Seq           int64  `gorm:"not null;default:0"`
	BatchId       string `gorm:"type:varchar(160);not null;default:''"`
	BatchBody     string `gorm:"type:text"`
	BatchMeta     string `gorm:"type:text"`
	Attempts      int    `gorm:"not null;default:0"`
	Owner         string `gorm:"type:varchar(64);not null;default:''"`
	LeaseUntil    int64  `gorm:"not null;default:0"`
	LastError     string `gorm:"type:varchar(255);not null;default:''"`
	LastSuccessAt int64  `gorm:"not null;default:0"`
	Accepted      int64  `gorm:"not null;default:0"`
	Unchanged     int64  `gorm:"not null;default:0"`
	Rejected      int64  `gorm:"not null;default:0"`
}

func (State) TableName() string { return "ops_user_sync_states" }

// Profile is what OPS last answered for one user: the hash of the pushed profile without its timestamps and balance.
type Profile struct {
	UserId    int64  `gorm:"primaryKey;autoIncrement:false"`
	Hash      string `gorm:"type:varchar(64);not null;default:''"`
	Status    string `gorm:"type:varchar(16);not null;default:''"`
	UpdatedAt int64  `gorm:"not null;default:0"`
	SentAt    int64  `gorm:"not null;default:0"`
}

func (Profile) TableName() string { return "ops_user_profiles" }

type Config struct {
	URL, KeyID, Secret string
	// Resend, when changed, pushes every user once more (for example at the balance opening moment, spec 5.4).
	Resend string
}

type Syncer struct {
	DB     *gorm.DB
	Config Config
	Client *http.Client
	Owner  string
	Now    func() time.Time
}

// StartConfigured starts the profile pusher when OPS_USER_SYNC_ENABLED=true.
func StartConfigured(ctx context.Context) error {
	if os.Getenv("OPS_USER_SYNC_ENABLED") != "true" {
		return nil
	}
	config := Config{URL: strings.TrimRight(os.Getenv("OPS_PUSH_URL"), "/"), KeyID: os.Getenv("OPS_PUSH_KEY_ID"),
		Secret: os.Getenv("OPS_PUSH_SECRET"), Resend: strings.TrimSpace(os.Getenv("OPS_USER_SYNC_RESEND"))}
	if err := (opspush.Config{URL: config.URL, KeyID: config.KeyID, Secret: config.Secret}).Validate(); err != nil {
		return errors.New("OPS_USER_SYNC_ENABLED needs a valid Ops push configuration: " + err.Error())
	}
	if len(config.Resend) > 64 {
		return errors.New("OPS_USER_SYNC_RESEND must be at most 64 characters")
	}
	syncer := New(model.DB, config)
	if err := syncer.Migrate(); err != nil {
		return err
	}
	go syncer.Run(ctx)
	common.SysLog("Ops user profile push enabled: " + config.URL + Path)
	return nil
}

func New(db *gorm.DB, config Config) *Syncer {
	pusher := opspush.New(db, nil, opspush.Config{})
	return &Syncer{DB: db, Config: config, Client: pusher.Client, Owner: pusher.Owner, Now: time.Now}
}

func (s *Syncer) Migrate() error {
	if err := s.DB.AutoMigrate(&State{}, &Profile{}); err != nil {
		return err
	}
	if common.UsingMainDatabase(common.DatabaseTypeMySQL) {
		// A batch of 500 profiles is over the 64 KB MySQL TEXT limit.
		for _, column := range []string{"batch_body", "batch_meta"} {
			if err := s.DB.Exec("ALTER TABLE ops_user_sync_states MODIFY " + column + " MEDIUMTEXT").Error; err != nil {
				return err
			}
		}
	}
	return s.DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&State{Id: 1, Resend: s.Config.Resend}).Error
}

func (s *Syncer) Run(ctx context.Context) {
	for {
		wait := s.Step()
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// Step does one unit of work and returns how long to wait before the next.
func (s *Syncer) Step() time.Duration {
	state, ok := s.lease()
	if !ok {
		return leaseSeconds * time.Second / 2
	}
	if state.BatchId == "" {
		if state.Resend != s.Config.Resend {
			// Forget what OPS has, so the next pass pushes every user again.
			if err := s.DB.Model(&Profile{}).Where("1 = 1").Update("hash", "").Error; err != nil {
				s.fail(state, "resend: "+err.Error())
				return stalledWait
			}
			state.Resend, state.ScanAfter = s.Config.Resend, 0
			if err := s.save(map[string]any{"resend": state.Resend, "scan_after": 0}); err != nil {
				return busyWait
			}
		}
		built, more, err := s.build(state)
		if err != nil {
			s.fail(state, "build: "+err.Error())
			return stalledWait
		}
		if !built {
			if more {
				return time.Second
			}
			return idleWait
		}
	}
	return s.send(state)
}

func (s *Syncer) lease() (*State, bool) {
	now := s.Now().Unix()
	err := s.DB.Model(&State{}).Where("id = 1 AND (owner = ? OR lease_until < ?)", s.Owner, now).
		Updates(map[string]any{"owner": s.Owner, "lease_until": now + leaseSeconds}).Error
	var state State
	if err != nil || s.DB.First(&state, 1).Error != nil || state.Owner != s.Owner {
		return nil, false
	}
	return &state, true
}

// save writes the given columns only while this instance still holds the lease.
func (s *Syncer) save(values map[string]any) error {
	if err := s.DB.Model(&State{}).Where("id = 1 AND owner = ?", s.Owner).Updates(values).Error; err != nil {
		return err
	}
	var owner string
	if err := s.DB.Model(&State{}).Where("id = 1").Select("owner").Scan(&owner).Error; err != nil || owner != s.Owner {
		return errors.New("lease lost")
	}
	return nil
}

func (s *Syncer) fail(state *State, message string) {
	if len(message) > 255 {
		message = message[:255]
	}
	state.Attempts++
	_ = s.save(map[string]any{"attempts": state.Attempts, "last_error": message})
	common.SysError("Ops user push: " + message)
}

type batch struct {
	SchemaVersion string           `json:"schemaVersion"`
	BatchId       string           `json:"batchId"`
	Users         []map[string]any `json:"users"`
}

// pending is one user in the persisted batch: what to store in ops_user_profiles once OPS answers 200.
type pending struct {
	UserId    int64  `json:"u"`
	Hash      string `json:"h"`
	Status    string `json:"s"`
	UpdatedAt int64  `json:"t"`
}

type candidate struct {
	pending
	profile map[string]any
}

type userRow struct {
	Id          int64
	Username    string
	DisplayName string
	Group       string
	Status      int
	Quota       int64
	CreatedAt   int64
	DeletedAt   gorm.DeletedAt
}

// build scans users after the cursor and persists the next batch of changed profiles. It returns built=false when
// nothing changed in the scanned range, with more=true when the pass has not reached the last user yet.
func (s *Syncer) build(state *State) (built, more bool, err error) {
	now := s.Now()
	after := state.ScanAfter
	agencies := s.DB.Migrator().HasTable(&model.AgencyActiveUserBinding{}) && s.DB.Migrator().HasTable(&model.Agency{})
	var changed []candidate
	for scanned := 0; scanned < scanPerStep && len(changed) < maxUsers; scanned += scanPage {
		var users []userRow
		if err := s.DB.Unscoped().Table("users").Select("id, username, display_name, "+groupColumn()+", status, quota, created_at, deleted_at").
			Where("id > ?", after).Order("id").Limit(scanPage).Find(&users).Error; err != nil {
			return false, false, err
		}
		end := len(users) < scanPage
		upper := int64(math.MaxInt64)
		if !end {
			upper = users[len(users)-1].Id
		}
		var stored []Profile
		if err := s.DB.Where("user_id > ? AND user_id <= ?", after, upper).Find(&stored).Error; err != nil {
			return false, false, err
		}
		known := make(map[int64]Profile, len(stored))
		for _, row := range stored {
			known[row.UserId] = row
		}
		distributors := map[int64]any{}
		if agencies {
			if distributors, err = s.distributors(users); err != nil {
				return false, false, err
			}
		}
		found := make([]candidate, 0)
		present := make(map[int64]bool, len(users))
		for _, user := range users {
			present[user.Id] = true
			profile := userProfile(user, distributors, agencies)
			hash := profileHash(profile)
			if row, ok := known[user.Id]; ok && row.Hash == hash {
				continue
			}
			found = append(found, candidate{pending{user.Id, hash, profile["status"].(string), now.Unix()}, profile})
		}
		// A user deleted from the table after OPS knew it is pushed once as removed.
		for _, row := range stored {
			if present[row.UserId] {
				continue
			}
			profile := map[string]any{"userId": strconv.FormatInt(row.UserId, 10), "status": "removed"}
			if hash := profileHash(profile); row.Hash != hash {
				found = append(found, candidate{pending{row.UserId, hash, "removed", now.Unix()}, profile})
			}
		}
		slices.SortFunc(found, func(a, b candidate) int { return cmp.Compare(a.UserId, b.UserId) })
		if room := maxUsers - len(changed); len(found) > room {
			found = found[:room]
			upper, end = found[len(found)-1].UserId, false
		}
		changed = append(changed, found...)
		if end {
			after = 0
			break
		}
		after = upper
	}
	if len(changed) == 0 {
		state.ScanAfter = after
		return false, after != 0, s.save(map[string]any{"scan_after": after})
	}
	for {
		state.Seq++
		// The random tail keeps ids unique if this table is ever recreated: OPS remembers every batchId (409 batch_conflict).
		tail := make([]byte, 3)
		_, _ = rand.Read(tail)
		id := fmt.Sprintf("users-%s-%06d-%s", now.UTC().Format("20060102"), state.Seq, hex.EncodeToString(tail))
		body, meta, err := encode(id, changed, now)
		if err != nil {
			return false, false, err
		}
		if len(body) > maxBody && len(changed) > 1 {
			changed = changed[:len(changed)/2]
			after = changed[len(changed)-1].UserId
			continue
		}
		state.ScanAfter, state.BatchId, state.BatchBody, state.BatchMeta, state.Attempts = after, id, string(body), string(meta), 0
		return true, true, s.save(map[string]any{"scan_after": after, "seq": state.Seq, "batch_id": id,
			"batch_body": state.BatchBody, "batch_meta": state.BatchMeta, "attempts": 0})
	}
}

func groupColumn() string {
	if common.UsingMainDatabase(common.DatabaseTypePostgreSQL) {
		return `"group"`
	}
	return "`group`"
}

// distributors maps each user bound to an Agency Hub agency to {"id","name"}; users without an agency map to nil.
func (s *Syncer) distributors(users []userRow) (map[int64]any, error) {
	result := make(map[int64]any, len(users))
	if len(users) == 0 {
		return result, nil
	}
	ids := make([]int64, len(users))
	for index, user := range users {
		ids[index] = user.Id
	}
	var rows []struct {
		UserId      int64
		AgencyId    int64
		DisplayName string
	}
	err := s.DB.Table(model.AgencyActiveUserBinding{}.TableName()+" AS b").
		Select("b.user_id, b.agency_id, a.display_name").
		Joins("LEFT JOIN "+model.Agency{}.TableName()+" AS a ON a.id = b.agency_id").
		Where("b.user_id IN ?", ids).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		distributor := map[string]any{"id": strconv.FormatInt(row.AgencyId, 10)}
		if name := strings.TrimSpace(row.DisplayName); name != "" && printable(name) {
			distributor["name"] = name
		}
		result[row.UserId] = distributor
	}
	return result, nil
}

// userProfile is the spec 11.2 profile without updatedAt and balance (they change without the profile changing).
// Contact details are not pushed. distributor is only sent when the Agency Hub tables exist.
func userProfile(user userRow, distributors map[int64]any, agencies bool) map[string]any {
	status := "disabled"
	switch {
	case user.DeletedAt.Valid:
		status = "removed"
	case user.Status == common.UserStatusEnabled:
		status = "active"
	}
	profile := map[string]any{"userId": strconv.FormatInt(user.Id, 10), "status": status}
	for name, value := range map[string]string{"username": user.Username, "displayName": user.DisplayName, "group": user.Group} {
		if value = strings.TrimSpace(value); value != "" && printable(value) {
			profile[name] = value
		}
	}
	if user.CreatedAt > 0 {
		profile["registeredAt"] = time.Unix(user.CreatedAt, 0).UTC().Format(time.RFC3339)
	}
	if agencies {
		profile["distributor"] = distributors[user.Id]
	}
	profile["quota"] = user.Quota // only to fill balance in encode; removed before hashing
	return profile
}

func printable(value string) bool {
	if len([]rune(value)) > maxText {
		return false
	}
	for _, r := range value {
		if !unicode.IsPrint(r) {
			return false
		}
	}
	return true
}

func profileHash(profile map[string]any) string {
	copied := make(map[string]any, len(profile))
	for key, value := range profile {
		if key != "quota" {
			copied[key] = value
		}
	}
	raw, _ := common.Marshal(copied)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// encode builds the request body: each profile gets updatedAt and, when the conversion is valid, a balance snapshot.
func encode(id string, changed []candidate, now time.Time) ([]byte, []byte, error) {
	perUnit, rate := common.QuotaPerUnit, operation_setting.USDExchangeRate
	balanced := perUnit >= 1 && perUnit == math.Trunc(perUnit) && perUnit <= math.MaxInt32 && rate > 0 && rate <= 1000
	at := now.UTC().Format(time.RFC3339)
	users := make([]map[string]any, len(changed))
	meta := make([]pending, len(changed))
	for index, item := range changed {
		user := make(map[string]any, len(item.profile)+2)
		for key, value := range item.profile {
			if key != "quota" {
				user[key] = value
			}
		}
		user["updatedAt"] = time.Unix(item.UpdatedAt, 0).UTC().Format(time.RFC3339)
		if quota, ok := item.profile["quota"].(int64); ok && balanced {
			user["balance"] = map[string]any{"quota": quota, "quotaPerUnit": int64(perUnit),
				"usdExchangeRate": strconv.FormatFloat(rate, 'f', -1, 64), "at": at}
		}
		users[index], meta[index] = user, item.pending
	}
	body, err := common.Marshal(batch{SchemaVersion: "1.1", BatchId: id, Users: users})
	if err != nil {
		return nil, nil, err
	}
	encodedMeta, err := common.Marshal(meta)
	return body, encodedMeta, err
}

type okBody struct {
	Data struct {
		Accepted  int `json:"accepted"`
		Unchanged int `json:"unchanged"`
	} `json:"data"`
}

type errorBody struct {
	Error struct {
		Code    string `json:"code"`
		Details struct {
			Rejected []struct {
				Index  int    `json:"index"`
				UserId string `json:"userId"`
				Reason string `json:"reason"`
			} `json:"rejected"`
		} `json:"details"`
	} `json:"error"`
}

func (s *Syncer) send(state *State) time.Duration {
	body := []byte(state.BatchBody)
	request, err := http.NewRequest(http.MethodPost, s.Config.URL+Path, bytes.NewReader(body))
	if err != nil {
		s.fail(state, err.Error())
		return stalledWait
	}
	for name, values := range opspush.Sign(s.Config.KeyID, s.Config.Secret, http.MethodPost, Path, body, s.Now()) {
		request.Header[name] = values
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := s.Client.Do(request)
	if err != nil {
		s.fail(state, "send "+state.BatchId+": "+err.Error())
		return opspush.Backoff(state.Attempts, 0)
	}
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	response.Body.Close()
	if response.StatusCode == http.StatusOK {
		var ok okBody
		_ = common.Unmarshal(raw, &ok)
		var meta []pending
		if err := common.UnmarshalJsonStr(state.BatchMeta, &meta); err != nil {
			s.fail(state, "stored batch unreadable: "+err.Error())
			return stalledWait
		}
		if err := s.remember(meta); err != nil {
			s.fail(state, "remember "+state.BatchId+": "+err.Error())
			return busyWait
		}
		_ = s.save(map[string]any{"batch_id": "", "batch_body": "", "batch_meta": "", "attempts": 0, "last_error": "",
			"last_success_at": s.Now().Unix(), "accepted": gorm.Expr("accepted + ?", ok.Data.Accepted),
			"unchanged": gorm.Expr("unchanged + ?", ok.Data.Unchanged)})
		return busyWait
	}
	var failure errorBody
	_ = common.Unmarshal(raw, &failure)
	code := failure.Error.Code
	if code == "" {
		code = http.StatusText(response.StatusCode)
	}
	message := fmt.Sprintf("send %s: HTTP %d %s", state.BatchId, response.StatusCode, code)
	switch {
	case response.StatusCode == http.StatusBadRequest && code == "invalid_users" && len(failure.Error.Details.Rejected) > 0:
		// Nothing was stored; the spec lets the same batchId be resent once corrected, so resend without the rejected users.
		s.dropRejected(state, failure)
		return busyWait
	case response.StatusCode == http.StatusTooManyRequests:
		retry, _ := strconv.Atoi(response.Header.Get("Retry-After"))
		s.fail(state, message)
		return opspush.Backoff(state.Attempts, time.Duration(retry)*time.Second)
	case response.StatusCode >= 500 || code == "timestamp_out_of_window" || code == "nonce_reused":
		s.fail(state, message)
		return opspush.Backoff(state.Attempts, 0)
	default:
		// Signature, credential, conflict or format errors need a person; keep the batch and look again later.
		s.fail(state, message)
		return stalledWait
	}
}

// remember records what OPS now holds for each user, so an unchanged user is not pushed again.
func (s *Syncer) remember(meta []pending) error {
	if len(meta) == 0 {
		return nil
	}
	rows := make([]Profile, len(meta))
	sent := s.Now().Unix()
	for index, item := range meta {
		rows[index] = Profile{UserId: item.UserId, Hash: item.Hash, Status: item.Status, UpdatedAt: item.UpdatedAt, SentAt: sent}
	}
	return s.DB.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "user_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"hash", "status", "updated_at", "sent_at"})}).
		CreateInBatches(rows, 100).Error
}

func (s *Syncer) dropRejected(state *State, failure errorBody) {
	var current batch
	var meta []pending
	if common.UnmarshalJsonStr(state.BatchBody, &current) != nil || common.UnmarshalJsonStr(state.BatchMeta, &meta) != nil || len(meta) != len(current.Users) {
		s.fail(state, "stored batch unreadable")
		return
	}
	drop := map[int]bool{}
	for _, item := range failure.Error.Details.Rejected {
		drop[item.Index] = true
		common.SysError(fmt.Sprintf("Ops user push: %s rejected user %s (%s)", state.BatchId, item.UserId, item.Reason))
	}
	var keptUsers []map[string]any
	var keptMeta, dropped []pending
	for index := range current.Users {
		if drop[index] {
			dropped = append(dropped, meta[index])
			continue
		}
		keptUsers, keptMeta = append(keptUsers, current.Users[index]), append(keptMeta, meta[index])
	}
	// A rejected profile is not retried until it changes again.
	if err := s.remember(dropped); err != nil {
		s.fail(state, "remember rejected: "+err.Error())
		return
	}
	if len(keptUsers) == 0 {
		_ = s.save(map[string]any{"batch_id": "", "batch_body": "", "batch_meta": "", "attempts": 0,
			"rejected": gorm.Expr("rejected + ?", len(dropped))})
		return
	}
	current.Users = keptUsers
	body, _ := common.Marshal(current)
	encodedMeta, _ := common.Marshal(keptMeta)
	_ = s.save(map[string]any{"batch_body": string(body), "batch_meta": string(encodedMeta), "rejected": gorm.Expr("rejected + ?", len(dropped))})
}
