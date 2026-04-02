package audit

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/joecattt/thaw/internal/snapshot"
	"github.com/joecattt/thaw/pkg/models"
)

// getChainKey returns a per-machine audit key.
// Generated on first use and stored in the data directory.
// Not cryptographically secret — just ensures chain is machine-specific.
func getChainKey() string {
	home, _ := os.UserHomeDir()
	keyFile := filepath.Join(home, ".local", "share", "thaw", "audit.key")

	data, err := os.ReadFile(keyFile)
	if err == nil && len(data) > 0 {
		return string(data)
	}

	// Generate new key: hostname + timestamp
	hostname, _ := os.Hostname()
	key := fmt.Sprintf("thaw-%s-%d", hostname, time.Now().UnixNano())

	os.MkdirAll(filepath.Dir(keyFile), 0700)
	os.WriteFile(keyFile, []byte(key), 0600)
	return key
}

// SignSnapshot computes the hash chain for a snapshot.
// Hash = HMAC(prevHash + snapshot data).
func SignSnapshot(snap *models.Snapshot, prevHash string) string {
	snap.PrevHash = prevHash

	data, _ := json.Marshal(struct {
		Sessions  []models.Session `json:"s"`
		CreatedAt time.Time        `json:"t"`
		Source    string           `json:"src"`
	}{snap.Sessions, snap.CreatedAt, snap.Source})

	mac := hmac.New(sha256.New, []byte(getChainKey()))
	mac.Write([]byte(prevHash))
	mac.Write(data)
	hash := hex.EncodeToString(mac.Sum(nil))

	snap.Hash = hash
	return hash
}

// Verify checks the integrity of the snapshot chain.
// Returns the first broken link, or nil if chain is intact.
func Verify(store *snapshot.Store) (*VerifyResult, error) {
	summaries, err := store.List(10000)
	if err != nil {
		return nil, err
	}

	result := &VerifyResult{Total: len(summaries)}

	prevHash := ""
	// Walk oldest to newest
	for i := len(summaries) - 1; i >= 0; i-- {
		snap, err := store.Get(summaries[i].ID)
		if err != nil || snap == nil {
			result.Errors = append(result.Errors, fmt.Sprintf("#%d: cannot load", summaries[i].ID))
			continue
		}

		if snap.Hash == "" {
			// Pre-audit snapshot — no hash, skip
			result.Unsigned++
			prevHash = ""
			continue
		}

		if snap.PrevHash != prevHash && prevHash != "" {
			result.Broken = append(result.Broken, snap.ID)
			result.Errors = append(result.Errors, fmt.Sprintf("#%d: prev_hash mismatch (chain broken)", snap.ID))
		}

		// Recompute and verify
		expected := recompute(snap, snap.PrevHash)
		if expected != snap.Hash {
			result.Tampered = append(result.Tampered, snap.ID)
			result.Errors = append(result.Errors, fmt.Sprintf("#%d: hash mismatch (data tampered)", snap.ID))
		} else {
			result.Verified++
		}

		prevHash = snap.Hash
	}

	return result, nil
}

type VerifyResult struct {
	Total    int
	Verified int
	Unsigned int
	Broken   []int
	Tampered []int
	Errors   []string
}

func (v *VerifyResult) IsIntact() bool {
	return len(v.Broken) == 0 && len(v.Tampered) == 0
}

func recompute(snap *models.Snapshot, prevHash string) string {
	data, _ := json.Marshal(struct {
		Sessions  []models.Session `json:"s"`
		CreatedAt time.Time        `json:"t"`
		Source    string           `json:"src"`
	}{snap.Sessions, snap.CreatedAt, snap.Source})

	mac := hmac.New(sha256.New, []byte(getChainKey()))
	mac.Write([]byte(prevHash))
	mac.Write(data)
	return hex.EncodeToString(mac.Sum(nil))
}

// ForgetTimeRange removes snapshot data within a time range but inserts a tombstone
// record to preserve the hash chain integrity.
func ForgetTimeRange(store *snapshot.Store, from, to time.Time) (int, error) {
	deleted, err := store.DeleteRange(from, to)
	if err != nil {
		return 0, err
	}

	// Insert tombstone so audit chain knows this was intentional, not tampering
	if deleted > 0 {
		store.InsertTombstone(from, to, deleted)
	}

	return deleted, nil
}
