package signin

import (
	"context"
	"errors"
	"strings"
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

	// prepareErr makes the agent-side opening sequence fail.
	prepareErr error
	// grants is what CreditGrants returns; nil means "no grants".
	grants []minimax.CreditGrant
	// grantsErr makes the grant read fail.
	grantsErr error
	// prepared records that the opening sequence ran, and calls records the
	// order of every upstream call so the sequence can be asserted rather than
	// assumed.
	prepared bool
	calls    []string
}

func (s *stubClient) note(name string) { s.calls = append(s.calls, name) }

func (s *stubClient) Prepare(context.Context, minimax.Credential) (*minimax.PrepareResult, error) {
	s.note("prepare")
	s.prepared = true
	if s.prepareErr != nil {
		return nil, s.prepareErr
	}
	return &minimax.PrepareResult{}, nil
}

func (s *stubClient) CreditGrants(context.Context, minimax.Credential) ([]minimax.CreditGrant, error) {
	s.note("grants")
	if s.grantsErr != nil {
		return nil, s.grantsErr
	}
	return s.grants, nil
}

func (s *stubClient) SigninStatus(context.Context, minimax.Credential) (*minimax.SigninPanel, error) {
	s.note("status")
	s.statusCall++
	if s.statusErr != nil {
		return nil, s.statusErr
	}
	return s.panel, nil
}

func (s *stubClient) SigninClaim(context.Context, minimax.Credential) (*minimax.SigninClaim, error) {
	s.note("claim")
	s.claimCall++
	if s.claimErr != nil {
		return nil, s.claimErr
	}
	return s.claim, nil
}

func (s *stubClient) Credit(context.Context, minimax.Credential) (*minimax.CreditInfo, error) {
	s.note("credit")
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

// TestStoredPanelReflectsTheClaim pins the board written after a claim.
//
// The panel is fetched *before* the claim, so storing it unchanged leaves the
// console showing a claimed status beside an empty dot for today. The upstream
// echoes a post-claim board in the claim response; when it does, that is what
// must be stored, and when it does not, today's slot still has to be marked.
func TestStoredPanelReflectsTheClaim(t *testing.T) {
	t.Run("prefers the board the claim echoed back", func(t *testing.T) {
		f := newFixture(t, defaultSettings())
		echoed := claimedPanel()
		// A scene the pre-claim fetch never returned, so the assertion can tell
		// the two boards apart rather than accepting either.
		echoed.Scene = 99
		f.client.claim = &minimax.SigninClaim{Result: 1, DayNo: 1, Points: 400, Panel: echoed}

		f.service.Sweep(context.Background())

		account, _ := f.store.AccountByID(f.account.ID)
		if account.SigninPanel == nil {
			t.Fatal("panel should be persisted")
		}
		if account.SigninPanel.Scene != 99 {
			t.Errorf("scene = %d, want 99 — the claim's board, not the earlier fetch",
				account.SigninPanel.Scene)
		}
		if account.SigninPanel.Days[0].Status != 3 {
			t.Errorf("today should read as claimed, got status %d", account.SigninPanel.Days[0].Status)
		}
	})

	t.Run("marks today when the claim carries no board", func(t *testing.T) {
		f := newFixture(t, defaultSettings())
		f.client.claim = &minimax.SigninClaim{Result: 1, DayNo: 1, Points: 400}

		f.service.Sweep(context.Background())

		account, _ := f.store.AccountByID(f.account.ID)
		if account.SigninPanel == nil {
			t.Fatal("panel should be persisted")
		}
		if account.SigninPanel.Days[0].Status != 3 {
			t.Errorf("today should be marked claimed, got status %d", account.SigninPanel.Days[0].Status)
		}
		if !account.SigninPanel.Days[0].IsToday {
			t.Error("the marked slot should be the one flagged as today")
		}
		// The fallback must not invent changes to days it was not told about.
		if account.SigninPanel.Days[1].Status != 1 {
			t.Errorf("tomorrow should be untouched, got status %d", account.SigninPanel.Days[1].Status)
		}
	})

	t.Run("a duplicate answer still marks the day", func(t *testing.T) {
		// Reached when the board said "unclaimed" but the claim says otherwise.
		// The claim is the authority on the claim, so the stale board loses.
		f := newFixture(t, defaultSettings())
		f.client.claim = &minimax.SigninClaim{
			Result: minimax.SigninClaimDuplicate, DayNo: 1, Points: 400,
		}

		f.service.Sweep(context.Background())

		account, _ := f.store.AccountByID(f.account.ID)
		if account.SigninStatus != store.SigninAlready {
			t.Errorf("status = %q, want %q", account.SigninStatus, store.SigninAlready)
		}
		if account.SigninPanel == nil || account.SigninPanel.Days[0].Status != 3 {
			t.Errorf("a duplicate answer must not leave today looking unclaimed, got %#v",
				account.SigninPanel)
		}
	})
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

// TestSweepSkipsAccountsWithoutUserID guards against a destructive
// misdiagnosis.
//
// The upstream answers a bare 401 both for a dead token and for a missing
// user_id, and a 401 is what retires an account as invalid. Attempting the call
// on an account whose realUserID was never resolved would therefore destroy a
// healthy account. Refusing up front keeps the account intact and says why.
func TestSweepSkipsAccountsWithoutUserID(t *testing.T) {
	for _, missing := range []string{"", "0"} {
		f := newFixture(t, defaultSettings())
		f.store.SaveAccountState(f.account.ID, func(target *store.Account) {
			target.UserID = missing
		})

		report := f.service.Sweep(context.Background())

		if report.Skipped != 1 {
			t.Errorf("UserID=%q: skipped = %d, want 1", missing, report.Skipped)
		}
		if f.client.statusCall != 0 {
			t.Errorf("UserID=%q: must not spend a request that is certain to 401, got %d calls",
				missing, f.client.statusCall)
		}

		account, _ := f.store.AccountByID(f.account.ID)
		if account.Status != store.StatusActive {
			t.Errorf("UserID=%q: the account must not be retired, got status %q",
				missing, account.Status)
		}
		if account.SigninStatus != store.SigninSkipped {
			t.Errorf("UserID=%q: status = %q, want %q",
				missing, account.SigninStatus, store.SigninSkipped)
		}
		if !strings.Contains(account.SigninError, "user_id") {
			t.Errorf("UserID=%q: the reason should name the missing field, got %q",
				missing, account.SigninError)
		}
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

// --- the agent-side opening sequence ---------------------------------------

// TestClaimWaitsForTheOpeningSequence pins an ordering rule that is easy to get
// wrong and impossible to notice once it is.
//
// The sequence creates the account's record on the agent side. A claim made
// before that record exists is registered and never paid out, while the endpoint
// still answers "success" — so getting the order wrong costs the day and looks
// exactly like getting it right.
func TestClaimWaitsForTheOpeningSequence(t *testing.T) {
	f := newFixture(t, defaultSettings())

	f.service.Sweep(context.Background())

	want := "prepare,status,claim,credit"
	if got := strings.Join(f.client.calls, ","); got != want {
		t.Errorf("upstream call order = %q, want %q", got, want)
	}
	if !f.client.prepared {
		t.Error("the opening sequence never ran")
	}
}

// TestAFailedOpeningSequenceSkipsTheClaim is the safety half of that rule.
//
// If the sequence cannot run, the claim is not attempted at all. A skipped day
// is visible and re-runnable; a claim in the wrong order loses the day and
// reports success, which is strictly worse.
func TestAFailedOpeningSequenceSkipsTheClaim(t *testing.T) {
	f := newFixture(t, defaultSettings())
	f.client.prepareErr = errors.New("upstream unreachable")

	report := f.service.Sweep(context.Background())

	if f.client.claimCall != 0 {
		t.Errorf("claim was attempted %d times despite a failed opening sequence", f.client.claimCall)
	}
	if f.client.statusCall != 0 {
		t.Errorf("the status read was attempted %d times; nothing past the sequence can be trusted",
			f.client.statusCall)
	}
	if report.Failed != 1 {
		t.Errorf("failed = %d, want 1", report.Failed)
	}
	account, _ := f.store.AccountByID(f.account.ID)
	if account.SigninStatus != store.SigninFailed {
		t.Errorf("status = %q, want %q", account.SigninStatus, store.SigninFailed)
	}
	if !strings.Contains(account.SigninError, "初始化") {
		t.Errorf("the reason should name the opening sequence, got %q", account.SigninError)
	}
	if account.Status != store.StatusActive {
		t.Errorf("an unreachable upstream is not a dead token; status = %q", account.Status)
	}
}

// --- reconciling -----------------------------------------------------------

// claimAgo backdates the account's last claim so the reconciliation grace period
// has elapsed.
func claimAgo(t *testing.T, f *fixture, d time.Duration, status string) {
	t.Helper()
	f.store.SaveAccountState(f.account.ID, func(target *store.Account) {
		target.SigninAt = time.Now().Add(-d)
		target.SigninStatus = status
	})
}

// TestReconcileFlagsAClaimThatNeverPaidOut pins the only defence against the
// silent failure the claim endpoint is capable of: it reports success whether or
// not the points were ever issued, so the credit grants are the only evidence
// that they were.
func TestReconcileFlagsAClaimThatNeverPaidOut(t *testing.T) {
	f := newFixture(t, defaultSettings())
	claimAgo(t, f, 30*time.Minute, store.SigninOK)
	f.client.grants = nil

	account, _ := f.store.AccountByID(f.account.ID)
	f.service.reconcile(context.Background(), account)

	updated, _ := f.store.AccountByID(f.account.ID)
	if updated.SigninStatus != store.SigninUnpaid {
		t.Errorf("status = %q, want %q", updated.SigninStatus, store.SigninUnpaid)
	}
	if !strings.Contains(updated.SigninError, "credit/details") {
		t.Errorf("the note should say where to look, got %q", updated.SigninError)
	}
}

// TestReconcileReadsTheGrantTimestampNotTheBalance keeps a thrifty account from
// being reported as unpaid: a grant that has been spent down to zero still
// proves the points arrived, and using them is not the same as never getting
// them.
func TestReconcileReadsTheGrantTimestampNotTheBalance(t *testing.T) {
	f := newFixture(t, defaultSettings())
	claimAgo(t, f, 30*time.Minute, store.SigninOK)
	f.client.grants = []minimax.CreditGrant{
		{GrantedAt: time.Now().Add(-29 * time.Minute), Granted: 400, Remaining: 0},
	}

	account, _ := f.store.AccountByID(f.account.ID)
	f.service.reconcile(context.Background(), account)

	updated, _ := f.store.AccountByID(f.account.ID)
	if updated.SigninStatus != store.SigninOK {
		t.Errorf("a fully spent grant still proves the payout; status = %q", updated.SigninStatus)
	}
}

// TestReconcileWaitsForThePayoutToAppear guards against the false alarm the
// visibility delay would otherwise cause. The grant list lags the claim — one
// measured payout took over a minute to show up — so judging straight after a
// claim reports healthy check-ins as unpaid.
func TestReconcileWaitsForThePayoutToAppear(t *testing.T) {
	f := newFixture(t, defaultSettings())
	claimAgo(t, f, time.Minute, store.SigninOK)

	account, _ := f.store.AccountByID(f.account.ID)
	f.service.reconcile(context.Background(), account)

	if len(f.client.calls) != 0 {
		t.Errorf("inside the grace period nothing should be read, got %v", f.client.calls)
	}
	updated, _ := f.store.AccountByID(f.account.ID)
	if updated.SigninStatus != store.SigninOK {
		t.Errorf("status = %q, want it left alone", updated.SigninStatus)
	}
}

// TestReconcileOnlyJudgesTodaysClaim: a grant list cannot say which day a
// missing payout belonged to, and an older one is already beyond recovery.
func TestReconcileOnlyJudgesTodaysClaim(t *testing.T) {
	f := newFixture(t, defaultSettings())
	claimAgo(t, f, 30*time.Hour, store.SigninOK)

	account, _ := f.store.AccountByID(f.account.ID)
	f.service.reconcile(context.Background(), account)

	if len(f.client.calls) != 0 {
		t.Errorf("yesterday's claim is not judgeable, got %v", f.client.calls)
	}
}

// TestReconcileClearsAFlagWhenTheGrantTurnsUp: the flag can be set by a pass
// that ran before the payout became visible, so it has to be retractable.
func TestReconcileClearsAFlagWhenTheGrantTurnsUp(t *testing.T) {
	f := newFixture(t, defaultSettings())
	claimAgo(t, f, 30*time.Minute, store.SigninUnpaid)
	f.store.SaveAccountState(f.account.ID, func(target *store.Account) {
		target.SigninError = "an earlier pass gave up too early"
	})
	f.client.grants = []minimax.CreditGrant{
		{GrantedAt: time.Now().Add(-30 * time.Minute), Granted: 400, Remaining: 400},
	}

	account, _ := f.store.AccountByID(f.account.ID)
	f.service.reconcile(context.Background(), account)

	updated, _ := f.store.AccountByID(f.account.ID)
	if updated.SigninStatus != store.SigninOK {
		t.Errorf("status = %q, want the flag cleared", updated.SigninStatus)
	}
	if updated.SigninError != "" {
		t.Errorf("the note should be cleared with it, got %q", updated.SigninError)
	}
}

// TestReconcileIgnoresAReadFailure: a hiccup in the grant endpoint says nothing
// about whether the points arrived, so it must not raise the flag.
func TestReconcileIgnoresAReadFailure(t *testing.T) {
	f := newFixture(t, defaultSettings())
	claimAgo(t, f, 30*time.Minute, store.SigninOK)
	f.client.grantsErr = errors.New("upstream unreachable")

	account, _ := f.store.AccountByID(f.account.ID)
	f.service.reconcile(context.Background(), account)

	updated, _ := f.store.AccountByID(f.account.ID)
	if updated.SigninStatus != store.SigninOK {
		t.Errorf("status = %q, want it left alone", updated.SigninStatus)
	}
	if updated.SigninError != "" {
		t.Errorf("a failed read must not write a note, got %q", updated.SigninError)
	}
}
