package signin

import (
	"context"
	"errors"
	"testing"
	"time"

	"minimax2api/internal/config"
	"minimax2api/internal/minimax"
	"minimax2api/internal/store"
)

// stubClient stands in for the upstream. The scheduler's job is sequencing and
// bookkeeping, so what matters here is which calls happen in what order and how
// the answers are recorded — not the HTTP layer, which signin_test.go in the
// minimax package pins byte for byte.
type stubClient struct {
	panel      *minimax.SigninPanel
	claim      *minimax.SigninClaim
	credit     *minimax.CreditInfo
	statusErr  error
	claimErr   error
	creditErr  error
	statusCall int
	claimCall  int
}

func (s *stubClient) SigninStatus(context.Context, minimax.Credential) (*minimax.SigninPanel, error) {
	s.statusCall++
	if s.statusErr != nil {
		return nil, s.statusErr
	}
	return s.panel, nil
}

func (s *stubClient) SigninClaim(context.Context, minimax.Credential) (*minimax.SigninClaim, error) {
	s.claimCall++
	if s.claimErr != nil {
		return nil, s.claimErr
	}
	return s.claim, nil
}

func (s *stubClient) Credit(context.Context, minimax.Credential) (*minimax.CreditInfo, error) {
	if s.creditErr != nil {
		return nil, s.creditErr
	}
	if s.credit == nil {
		return &minimax.CreditInfo{Total: 400}, nil
	}
	return s.credit, nil
}

func unclaimedPanel() *minimax.SigninPanel {
	return &minimax.SigninPanel{
		Scene: 2,
		Days: []minimax.SigninDay{
			{DayNo: 1, Points: 400, Status: 1, IsToday: true},
			{DayNo: 2, Points: 400, Status: 1},
		},
		TodayDayNo: 1, TodayPoints: 400,
	}
}

func claimedPanel() *minimax.SigninPanel {
	panel := unclaimedPanel()
	panel.Days[0].Status = 3
	panel.ClaimedToday = true
	return panel
}

type fixture struct {
	service *Service
	store   *store.Store
	client  *stubClient
	account *store.Account
}

func newFixture(t *testing.T, settings *config.Settings) *fixture {
	t.Helper()
	st, err := store.Open(t.TempDir(), "admin", "admin12345")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	// The store writes on a debounce, so without an explicit close its final
	// flush lands after t.TempDir() has been removed and logs a spurious
	// persistence failure.
	t.Cleanup(func() { _ = st.Close() })
	client := &stubClient{panel: unclaimedPanel(), claim: &minimax.SigninClaim{Result: 1, DayNo: 1, Points: 400}}

	settingsFn := func() config.Settings { return *settings }
	service := New(st, client, settingsFn, func(account *store.Account) minimax.Credential {
		return minimax.Credential{
			Region: account.Region, Token: account.Token, UserID: account.UserID,
			UUID: account.UUID, DeviceID: account.DeviceID,
		}
	})

	account := &store.Account{
		Name: "acc", Enabled: true, Region: store.RegionGlobal,
		Token: "token-" + t.Name(), UUID: "uuid-x", DeviceID: "device-x", UserID: "1",
	}
	if err := st.AddAccount(account); err != nil {
		t.Fatalf("add account: %v", err)
	}
	return &fixture{service: service, store: st, client: client, account: account}
}

func defaultSettings() *config.Settings {
	settings := config.DefaultSettings("testdata")
	settings.Signin.Enabled = true
	settings.Signin.Hour = 9
	settings.Signin.Minute = 5
	settings.Signin.GapSeconds = 0
	return &settings
}

// TestNextRunCatchesUpAMissedSlot pins the behaviour that makes the scheduler
// worth having: a machine that was asleep at the scheduled minute must still
// check in when it wakes, rather than silently losing the day.
func TestNextRunCatchesUpAMissedSlot(t *testing.T) {
	settings := defaultSettings()
	f := newFixture(t, settings)

	// 08:00, before the 09:05 slot: wait for it.
	at := time.Date(2026, 9, 18, 8, 0, 0, 0, time.Local)
	want := time.Date(2026, 9, 18, 9, 5, 0, 0, time.Local)
	if got := f.service.NextRun(at); !got.Equal(want) {
		t.Errorf("before the slot NextRun = %v, want %v", got, want)
	}

	// 15:00, after the slot and never served: due now.
	at = time.Date(2026, 9, 18, 15, 0, 0, 0, time.Local)
	if got := f.service.NextRun(at); !got.Equal(at) {
		t.Errorf("a missed slot should be due immediately, got %v", got)
	}

	// Mark today as served, then the next slot is tomorrow.
	f.store.SaveAccountState(f.account.ID, func(target *store.Account) {
		target.SigninAt = time.Date(2026, 9, 18, 15, 0, 30, 0, time.Local)
	})
	want = time.Date(2026, 9, 19, 9, 5, 0, 0, time.Local)
	if got := f.service.NextRun(at); !got.Equal(want) {
		t.Errorf("after a served day NextRun = %v, want %v", got, want)
	}
}

// TestSweepClaimsWhenUnclaimed is the happy path.
func TestSweepClaimsWhenUnclaimed(t *testing.T) {
	f := newFixture(t, defaultSettings())

	report := f.service.Sweep(context.Background())
	if report == nil {
		t.Fatal("Sweep returned nil")
	}
	if report.Claimed != 1 || report.Points != 400 {
		t.Errorf("claimed = %d, points = %d, want 1 and 400", report.Claimed, report.Points)
	}
	if f.client.claimCall != 1 {
		t.Errorf("claim calls = %d, want 1", f.client.claimCall)
	}

	account, _ := f.store.AccountByID(f.account.ID)
	if account.SigninStatus != store.SigninOK {
		t.Errorf("status = %q, want %q", account.SigninStatus, store.SigninOK)
	}
	if account.SigninTotal != 400 {
		t.Errorf("cumulative points = %d, want 400", account.SigninTotal)
	}
	if account.SigninStreak != 1 {
		t.Errorf("streak = %d, want 1", account.SigninStreak)
	}
	if account.SigninAt.IsZero() {
		t.Error("SigninAt should be stamped so the day counts as served")
	}
	if account.SigninPanel == nil || len(account.SigninPanel.Days) != 2 {
		t.Errorf("panel should be persisted, got %#v", account.SigninPanel)
	}
	if account.Credit == nil || account.Credit.Total != 400 {
		t.Errorf("credit should be refreshed after a claim, got %#v", account.Credit)
	}
}

// TestSweepSkipsClaimWhenAlreadyDone asserts the status call actually gates the
// claim: claiming every day regardless would be a needless second request
// against a risk-control sensitive endpoint.
func TestSweepSkipsClaimWhenAlreadyDone(t *testing.T) {
	f := newFixture(t, defaultSettings())
	f.client.panel = claimedPanel()

	report := f.service.Sweep(context.Background())
	if report.Already != 1 || report.Claimed != 0 {
		t.Errorf("already = %d, claimed = %d, want 1 and 0", report.Already, report.Claimed)
	}
	if f.client.claimCall != 0 {
		t.Errorf("claim calls = %d, want 0 when today is already served", f.client.claimCall)
	}

	account, _ := f.store.AccountByID(f.account.ID)
	if account.SigninStatus != store.SigninAlready {
		t.Errorf("status = %q, want %q", account.SigninStatus, store.SigninAlready)
	}
	if account.SigninTotal != 0 {
		t.Errorf("an already-claimed day must not add to the total, got %d", account.SigninTotal)
	}
}

// TestSweepCountsDuplicateClaimAsAlready covers the idempotent answer: a second
// claim in one day returns result 2, which is success, not a failure.
func TestSweepCountsDuplicateClaimAsAlready(t *testing.T) {
	f := newFixture(t, defaultSettings())
	f.client.claim = &minimax.SigninClaim{Result: minimax.SigninClaimDuplicate, DayNo: 1, Points: 400}

	report := f.service.Sweep(context.Background())
	if report.Already != 1 || report.Failed != 0 {
		t.Errorf("already = %d, failed = %d, want 1 and 0", report.Already, report.Failed)
	}

	account, _ := f.store.AccountByID(f.account.ID)
	if account.SigninTotal != 0 {
		t.Errorf("a duplicate claim must not double-count points, got %d", account.SigninTotal)
	}
}

// TestSweepSkipsMainlandAccounts documents that the mainland deployment speaks a
// different check-in protocol. Signing one with the international shape would
// earn a rejection that looks exactly like a dead token, so the honest answer is
// to skip it.
func TestSweepSkipsMainlandAccounts(t *testing.T) {
	f := newFixture(t, defaultSettings())
	f.store.SaveAccountState(f.account.ID, func(target *store.Account) {
		target.Region = store.RegionCN
	})

	report := f.service.Sweep(context.Background())
	if report.Skipped != 1 {
		t.Errorf("skipped = %d, want 1", report.Skipped)
	}
	if f.client.statusCall != 0 {
		t.Errorf("a mainland account must not be sent to the international endpoint, got %d calls", f.client.statusCall)
	}

	account, _ := f.store.AccountByID(f.account.ID)
	if account.SigninStatus != store.SigninSkipped {
		t.Errorf("status = %q, want %q", account.SigninStatus, store.SigninSkipped)
	}
	if account.Status != store.StatusActive {
		t.Errorf("skipping must not disturb the account's health, got %q", account.Status)
	}
}

// TestSweepRetiresRejectedToken asserts an expired token is retired rather than
// merely cooled down: retrying it cannot succeed, and the cooldown would hide a
// problem the operator needs to see.
func TestSweepRetiresRejectedToken(t *testing.T) {
	f := newFixture(t, defaultSettings())
	f.client.statusErr = minimax.ErrInvalidCredential

	report := f.service.Sweep(context.Background())
	if report.Failed != 1 {
		t.Errorf("failed = %d, want 1", report.Failed)
	}

	account, _ := f.store.AccountByID(f.account.ID)
	if account.Status != store.StatusInvalid {
		t.Errorf("status = %q, want %q", account.Status, store.StatusInvalid)
	}
}

// TestFailedAttemptStillStampsTheDay guards the retry loop: if a failed sweep
// left no trace, the scheduler would treat the slot as unserved and fire again
// on the next tick, turning an upstream outage into a request storm.
func TestFailedAttemptStillStampsTheDay(t *testing.T) {
	f := newFixture(t, defaultSettings())
	f.client.statusErr = errors.New("upstream is down")

	f.service.Sweep(context.Background())

	account, _ := f.store.AccountByID(f.account.ID)
	if account.SigninAt.IsZero() {
		t.Fatal("a failed attempt must still stamp SigninAt")
	}
	if account.SigninStatus != store.SigninFailed {
		t.Errorf("status = %q, want %q", account.SigninStatus, store.SigninFailed)
	}
	if account.SigninError == "" {
		t.Error("the failure reason should be recorded for the console")
	}

	// And the day now reads as served, so the slot is not reconsidered. The
	// probe time is derived from the stamp rather than hard-coded: the sweep
	// stamps the real clock, and a fixed date would compare against the wrong
	// calendar day and pass for the wrong reason.
	stamp := account.SigninAt
	at := time.Date(stamp.Year(), stamp.Month(), stamp.Day(), 23, 0, 0, 0, stamp.Location())
	if got := f.service.NextRun(at); got.Equal(at) {
		t.Error("a failed sweep should still count the day as served")
	}
}

// TestCreditFailureDoesNotRetireTheAccount: the balance endpoint is a separate
// service, and its hiccup says nothing about whether the token works.
func TestCreditFailureDoesNotRetireTheAccount(t *testing.T) {
	f := newFixture(t, defaultSettings())
	f.client.creditErr = errors.New("balance endpoint down")

	f.service.Sweep(context.Background())

	account, _ := f.store.AccountByID(f.account.ID)
	if account.SigninStatus != store.SigninOK {
		t.Errorf("status = %q, want %q", account.SigninStatus, store.SigninOK)
	}
	if account.Status != store.StatusActive {
		t.Errorf("a balance read failure must not change account health, got %q", account.Status)
	}
}

// TestSweepIsNotReentrant: a manual trigger landing on top of the scheduled run
// must report "nothing happened" rather than queue a second pass.
func TestSweepIsNotReentrant(t *testing.T) {
	f := newFixture(t, defaultSettings())

	// Hold the flag directly rather than racing a real sweep, so the assertion
	// is deterministic.
	f.service.mu.Lock()
	f.service.running = true
	f.service.mu.Unlock()

	if report := f.service.Sweep(context.Background()); report != nil {
		t.Errorf("a concurrent sweep should return nil, got %#v", report)
	}
	if f.client.statusCall != 0 {
		t.Errorf("no upstream call should be made, got %d", f.client.statusCall)
	}
}

// TestRefreshCreditParsesStoredValue checks the manual refresh path used by the
// console.
func TestRefreshCreditParsesStoredValue(t *testing.T) {
	f := newFixture(t, defaultSettings())
	f.client.credit = &minimax.CreditInfo{Total: 1234, Free: 1000, Purchased: 234, PlanName: "Pro"}

	credit, err := f.service.RefreshCredit(context.Background(), f.account.ID)
	if err != nil {
		t.Fatalf("RefreshCredit: %v", err)
	}
	if credit.Total != 1234 {
		t.Errorf("total = %d, want 1234", credit.Total)
	}

	account, _ := f.store.AccountByID(f.account.ID)
	if account.Credit == nil || account.Credit.Total != 1234 {
		t.Errorf("credit should be persisted, got %#v", account.Credit)
	}
}
