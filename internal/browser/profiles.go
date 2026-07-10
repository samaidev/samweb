package browser

import (
        "encoding/json"
        "fmt"
        "os"
        "path/filepath"
        "sort"
        "sync"
        "time"

        "github.com/samaidev/samweb/internal/cdp"
)

// Profile is a named cookie set. Each profile stores a snapshot of all
// CDP browser cookies at the time it was saved, allowing the user to
// switch between multiple accounts on the same site (e.g. z.ai) within
// a single samweb instance.
type Profile struct {
        ID      string        `json:"id"`       // unique identifier (slugified name or uuid)
        Name    string        `json:"name"`     // user-visible label (mutable)
        Cookies []cdp.CDPCookie `json:"cookies"` // snapshot of all CDP cookies
        Created int64         `json:"created"`  // unix seconds
        Updated int64         `json:"updated"`  // unix seconds

        // LocalStorage is a snapshot of localStorage entries, keyed by
        // origin (e.g. "https://chat.z.ai"). Each value is a map of
        // key→value strings. This is critical for sites like z.ai that
        // store their login JWT in localStorage, not just cookies.
        LocalStorage map[string]map[string]string `json:"local_storage,omitempty"`

        // AICQ identity for this profile. When non-empty, the main
        // samweb spawns an AICQ bridge alongside the tab worker to
        // connect AICQ messages to z.ai.
        AICQIdentity *AICQIdentity `json:"aicq_identity,omitempty"`

        // ChatMappings maps AICQ friend_id → z.ai chat_id.
        ChatMappings map[string]string `json:"chat_mappings,omitempty"`
}

// AICQIdentity stores the AICQ agent identity for a profile.
type AICQIdentity struct {
        AccountID   string `json:"account_id"`
        SigningPub  string `json:"signing_pub"`
        SigningSec  string `json:"signing_sec"`
        ExchangePub string `json:"exchange_pub"`
        ExchangeSec string `json:"exchange_sec"`
        OwnerID     string `json:"owner_id"`
        DBPath      string `json:"db_path"` // path to the AICQ SDK db file
}

// profilesFile is the on-disk JSON file for profile storage.
// Defaults to ~/.samweb/profiles.json (alongside the proxy cookie jar).
func profilesFile() string {
        home, err := os.UserHomeDir()
        if err != nil || home == "" {
                home = "."
        }
        return filepath.Join(home, ".samweb", "profiles.json")
}

// profilesState is the on-disk representation.
type profilesState struct {
        Profiles        []Profile `json:"profiles"`
        ActiveProfileID string    `json:"active_profile_id"` // may be "" (no profile = default)
}

// ProfileStore is the in-memory + on-disk profile manager.
// It is goroutine-safe.
type ProfileStore struct {
        mu     sync.RWMutex
        state  profilesState
        loaded bool
}

// globalProfiles is the singleton ProfileStore used by the browser backend.
var globalProfiles = &ProfileStore{}

// Profiles returns the singleton ProfileStore.
func Profiles() *ProfileStore {
        return globalProfiles
}

// load reads the on-disk profile state into memory. It is idempotent
// and called lazily by all other methods.
func (s *ProfileStore) load() error {
        s.mu.Lock()
        defer s.mu.Unlock()
        if s.loaded {
                return nil
        }
        data, err := os.ReadFile(profilesFile())
        if err != nil {
                if os.IsNotExist(err) {
                        s.state = profilesState{Profiles: []Profile{}}
                        s.loaded = true
                        return nil
                }
                return err
        }
        if err := json.Unmarshal(data, &s.state); err != nil {
                return err
        }
        if s.state.Profiles == nil {
                s.state.Profiles = []Profile{}
        }
        s.loaded = true
        return nil
}

// save writes the in-memory state to disk atomically.
// The caller MUST hold s.mu (either read or write lock) — this function
// does not acquire the lock itself to avoid re-entrant locking when
// called from Create/Rename/Delete/Activate (which already hold the
// write lock).
func (s *ProfileStore) save() error {
        path := profilesFile()
        if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
                return err
        }
        data, err := json.MarshalIndent(s.state, "", "  ")
        if err != nil {
                return err
        }
        tmp := path + ".tmp"
        if err := os.WriteFile(tmp, data, 0o600); err != nil {
                return err
        }
        return os.Rename(tmp, path)
}

// List returns all profiles, sorted by Created (oldest first).
// The currently active profile (if any) is included.
func (s *ProfileStore) List() ([]Profile, string, error) {
        if err := s.load(); err != nil {
                return nil, "", err
        }
        s.mu.RLock()
        defer s.mu.RUnlock()
        out := make([]Profile, len(s.state.Profiles))
        copy(out, s.state.Profiles)
        sort.Slice(out, func(i, j int) bool { return out[i].Created < out[j].Created })
        return out, s.state.ActiveProfileID, nil
}

// Create makes a new profile with the given name and saves the current
// CDP cookies into it. Returns the new profile.
//
// If a profile with the same name already exists, it is updated (its
// cookies are replaced with the current snapshot).
func (s *ProfileStore) Create(name string, cookies []cdp.CDPCookie) (Profile, error) {
        if err := s.load(); err != nil {
                return Profile{}, err
        }
        s.mu.Lock()
        defer s.mu.Unlock()

        // Find existing profile with same name (case-insensitive)
        for i, p := range s.state.Profiles {
                if equalFoldName(p.Name, name) {
                        s.state.Profiles[i].Cookies = cloneCookies(cookies)
                        s.state.Profiles[i].Updated = nowUnix()
                        prof := s.state.Profiles[i]
                        return prof, s.save()
                }
        }

        // Create new
        id := slugify(name)
        if id == "" {
                id = fmt.Sprintf("profile-%d", len(s.state.Profiles))
        }
        // Ensure unique id
        idBase := id
        for n := 1; ; n++ {
                exists := false
                for _, p := range s.state.Profiles {
                        if p.ID == id {
                                exists = true
                                break
                        }
                }
                if !exists {
                        break
                }
                id = fmt.Sprintf("%s-%d", idBase, n)
        }
        now := nowUnix()
        prof := Profile{
                ID:      id,
                Name:    name,
                Cookies: cloneCookies(cookies),
                Created: now,
                Updated: now,
        }
        s.state.Profiles = append(s.state.Profiles, prof)
        return prof, s.save()
}

// Rename changes the user-visible name of a profile. The ID is preserved.
func (s *ProfileStore) Rename(id, newName string) error {
        if err := s.load(); err != nil {
                return err
        }
        s.mu.Lock()
        defer s.mu.Unlock()
        for i, p := range s.state.Profiles {
                if p.ID == id {
                        s.state.Profiles[i].Name = newName
                        s.state.Profiles[i].Updated = nowUnix()
                        return s.save()
                }
        }
        return fmt.Errorf("profile not found: %s", id)
}

// Delete removes a profile. If the deleted profile was active, the
// active profile is cleared (no profile selected).
func (s *ProfileStore) Delete(id string) error {
        if err := s.load(); err != nil {
                return err
        }
        s.mu.Lock()
        defer s.mu.Unlock()
        for i, p := range s.state.Profiles {
                if p.ID == id {
                        s.state.Profiles = append(s.state.Profiles[:i], s.state.Profiles[i+1:]...)
                        if s.state.ActiveProfileID == id {
                                s.state.ActiveProfileID = ""
                        }
                        return s.save()
                }
        }
        return fmt.Errorf("profile not found: %s", id)
}

// Activate marks a profile as the currently-active one. The caller is
// responsible for actually loading the profile's cookies into the browser.
// Pass an empty id to clear the active profile.
func (s *ProfileStore) Activate(id string) error {
        if err := s.load(); err != nil {
                return err
        }
        s.mu.Lock()
        defer s.mu.Unlock()
        if id != "" {
                found := false
                for _, p := range s.state.Profiles {
                        if p.ID == id {
                                found = true
                                break
                        }
                }
                if !found {
                        return fmt.Errorf("profile not found: %s", id)
                }
        }
        s.state.ActiveProfileID = id
        return s.save()
}

// Get returns a profile by ID.
func (s *ProfileStore) Get(id string) (Profile, bool, error) {
        if err := s.load(); err != nil {
                return Profile{}, false, err
        }
        s.mu.RLock()
        defer s.mu.RUnlock()
        for _, p := range s.state.Profiles {
                if p.ID == id {
                        return p, true, nil
                }
        }
        return Profile{}, false, nil
}

// UpdateCookies replaces the cookies stored in a profile with the given
// snapshot. Used when the user re-saves the current cookies into an
// existing profile.
func (s *ProfileStore) UpdateCookies(id string, cookies []cdp.CDPCookie) error {
        if err := s.load(); err != nil {
                return err
        }
        s.mu.Lock()
        defer s.mu.Unlock()
        for i, p := range s.state.Profiles {
                if p.ID == id {
                        s.state.Profiles[i].Cookies = cloneCookies(cookies)
                        s.state.Profiles[i].Updated = nowUnix()
                        return s.save()
                }
        }
        return fmt.Errorf("profile not found: %s", id)
}

// UpdateLocalStorage replaces the localStorage snapshot stored in a
// profile. Used together with UpdateCookies when the user re-saves the
// current state into an existing profile.
func (s *ProfileStore) UpdateLocalStorage(id string, ls map[string]map[string]string) error {
        if err := s.load(); err != nil {
                return err
        }
        s.mu.Lock()
        defer s.mu.Unlock()
        for i, p := range s.state.Profiles {
                if p.ID == id {
                        s.state.Profiles[i].LocalStorage = ls
                        s.state.Profiles[i].Updated = nowUnix()
                        return s.save()
                }
        }
        return fmt.Errorf("profile not found: %s", id)
}

// cloneCookies returns a deep copy of the given cookie slice so the
// caller cannot mutate the stored snapshot via aliasing.
func cloneCookies(in []cdp.CDPCookie) []cdp.CDPCookie {
        if in == nil {
                return nil
        }
        out := make([]cdp.CDPCookie, len(in))
        copy(out, in)
        return out
}

// equalFoldName compares two profile names case-insensitively.
func equalFoldName(a, b string) bool {
        // Avoid unicode/strings import; ASCII case-fold is enough for profile names.
        if len(a) != len(b) {
                return false
        }
        for i := 0; i < len(a); i++ {
                ca, cb := a[i], b[i]
                if ca >= 'A' && ca <= 'Z' {
                        ca += 'a' - 'A'
                }
                if cb >= 'A' && cb <= 'Z' {
                        cb += 'a' - 'A'
                }
                if ca != cb {
                        return false
                }
        }
        return true
}

// slugify converts a name to a filesystem-safe slug suitable for use
// as a profile ID. Non-alphanumeric characters are replaced with '-'.
func slugify(name string) string {
        out := make([]byte, 0, len(name))
        prevDash := false
        for i := 0; i < len(name); i++ {
                c := name[i]
                if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
                        out = append(out, c)
                        prevDash = false
                } else if c >= 'A' && c <= 'Z' {
                        out = append(out, c+('a'-'A'))
                        prevDash = false
                } else {
                        if !prevDash && len(out) > 0 {
                                out = append(out, '-')
                                prevDash = true
                        }
                }
        }
        // trim trailing dash
        for len(out) > 0 && out[len(out)-1] == '-' {
                out = out[:len(out)-1]
        }
        return string(out)
}

// nowUnix returns the current time in seconds since epoch.
func nowUnix() int64 {
        return time.Now().Unix()
}
