// Package signin drives the daily MiniMax check-in and the credit readings
// that keep spent accounts out of rotation.
//
// Only the international deployment (agent.minimax.io) speaks the protocol
// implemented in internal/minimax. Mainland accounts share the same product but
// a different check-in parameter set, so they are reported as skipped rather
// than signed with the wrong shape — a rejection there would be indistinguishable
// from a genuinely broken token and would retire a healthy account.
package signin

import (
	"context"
	"errors"
	"log"
	"strings"
	"sync"
	"time"

	"minimax2api/internal/config"
	"minimax2api/internal/minimax"
	"minimax2api/internal/store"
)

// Client is the slice of the upstream client this package depends on. Declaring
// it here rather than taking *minimax.Client keeps the scheduler testable with a
// stub and free of any dependency on the transport layer.
type Client interface {
	SigninStatus(ctx context.Context, cred minimax.Credential) (*minimax.SigninPanel, error)
	SigninClaim(ctx context.Context, cred minimax.Credential) (*minimax.SigninClaim, error)
	Credit(ctx context.Context, cred minimax.Credential) (*minimax.CreditInfo, error)
}

// AccountResult is one account's outcome within a sweep.
type AccountResult struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
	Points int    `json:"points"`
	Streak int    `json:"streak"`
	Credit int    `json:"credit"`
	Error  string `json:"error"`
}

// Report summarises a sweep.
type Report struct {
	StartedAt time.Time       `json:"startedAt"`
	EndedAt   time.Time       `json:"endedAt"`
	Total     int             `json:"total"`
	Claimed   int             `json:"claimed"`
	Already   int             `json:"already"`
	Failed    int             `json:"failed"`
	Skipped   int             `json:"skipped"`
	Points    int             `json:"points"`
	Results   []AccountResult `json:"results"`
}

// Service owns the daily timer and the credit poller.
type Service struct {
	store    *store.Store
	client   Client
	settings func() config.Settings
	credOf   func(*store.Account) minimax.Credential

	mu      sync.Mutex
	running bool
	last    *Report
	lastAt  time.Time
}

// New builds a Service. credOf converts a stored account into the credential
// the upstream client expects; it is injected so this package does not have to
// import the gateway.
func New(st *store.Store, client Client, settings func() config.Settings, credOf func(*store.Account) minimax.Credential) *Service {
	return &Service{store: st, client: client, settings: settings, credOf: credOf}
}

// Start launches the background loops. They stop when ctx is cancelled.
func (s *Service) Start(ctx context.Context) {
	go s.loop(ctx)
	go s.creditLoop(ctx)
}

// LastReport returns the most recent sweep, if any.
func (s *Service) LastReport() *Report {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last
}

// LastRunAt reports when the last sweep finished.
func (s *Service) LastRunAt() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastAt
}

// Running reports whether a sweep is in flight.
func (s *Service) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

// ------------------------------------------------------------------ schedule

// slotOn is the configured check-in instant on the calendar day containing t.
func (s *Service) slotOn(t time.Time) time.Time {
	midnight := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
	return midnight.Add(s.settings().SigninClock())
}

// NextRun is the next instant the sweep should fire.
//
// A slot that has already passed but was never served is due immediately rather
// than skipped. Without that, a machine that was asleep at the scheduled minute
// would silently miss the day, and a check-in tool that misses days is not worth
// scheduling.
func (s *Service) NextRun(now time.Time) time.Time {
	slot := s.slotOn(now)
	if now.Before(slot) {
		return slot
	}
	if s.sweptOn(now) {
		return slot.Add(24 * time.Hour)
	}
	return now
}

// sweptOn reports whether any account records a check-in attempt on the
// calendar day containing t. Deriving it from the account records avoids a
// second piece of persisted state that could disagree with them.
func (s *Service) sweptOn(t time.Time) bool {
	year, month, day := t.Date()
	for _, account := range s.store.ListAccounts() {
		if account.SigninAt.IsZero() {
			continue
		}
		local := account.SigninAt.In(t.Location())
		accountYear, accountMonth, accountDay := local.Date()
		if accountYear == year && accountMonth == month && accountDay == day {
			return true
		}
	}
	return false
}

func (s *Service) loop(ctx context.Context) {
	for {
		next := s.NextRun(time.Now())
		if !sleep(ctx, time.Until(next)) {
			return
		}
		s.Sweep(ctx)
		// Move straight to tomorrow's slot. Recomputing from NextRun would spin
		// here whenever the sweep legitimately marks nothing — an empty pool, or
		// a pool of accounts that were all skipped.
		if !sleep(ctx, time.Until(s.slotOn(time.Now().Add(24*time.Hour)))) {
			return
		}
	}
}

// sleep waits for d, reporting false if ctx was cancelled first. A zero or
// negative duration returns immediately.
func sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// -------------------------------------------------------------------- sweep

// Sweep runs one check-in pass over the pool.
//
// Accounts are visited one at a time with a gap between them. Check-in sits
// behind the same risk control as everything else on the site, and a burst of
// simultaneous requests from a single address is the pattern that attracts it.
func (s *Service) Sweep(ctx context.Context) *Report {
	s.mu.Lock()
	if s.running {
		// Report "nothing happened" rather than the previous run: the console
		// needs to tell the operator that their click was absorbed by an
		// in-flight sweep, not that it completed instantly.
		s.mu.Unlock()
		return nil
	}
	s.running = true
	s.mu.Unlock()

	report := &Report{StartedAt: time.Now()}
	defer func() {
		report.EndedAt = time.Now()
		s.mu.Lock()
		s.running = false
		s.last = report
		s.lastAt = report.EndedAt
		s.mu.Unlock()
	}()

	accounts := s.store.ListAccounts()
	gap := s.settings().SigninGap()

	for index, account := range accounts {
		if ctx.Err() != nil {
			break
		}
		result := s.checkAccount(ctx, account)
		report.Total++
		switch result.Status {
		case store.SigninOK:
			report.Claimed++
			report.Points += result.Points
		case store.SigninAlready:
			report.Already++
		case store.SigninSkipped:
			report.Skipped++
		default:
			report.Failed++
		}
		report.Results = append(report.Results, result)
		if index < len(accounts)-1 && gap > 0 {
			if !sleep(ctx, gap) {
				break
			}
		}
	}
	log.Printf("签到完成：共 %d，新领 %d，已领 %d，失败 %d，跳过 %d，本次积分 %d",
		report.Total, report.Claimed, report.Already, report.Failed, report.Skipped, report.Points)
	return report
}

// CheckOne runs the check-in for a single account, for the console's manual
// trigger.
func (s *Service) CheckOne(ctx context.Context, id string) (AccountResult, error) {
	account, ok := s.store.AccountByID(id)
	if !ok {
		return AccountResult{}, errors.New("账号不存在")
	}
	return s.checkAccount(ctx, account), nil
}

// checkAccount performs the status-then-claim sequence for one account.
func (s *Service) checkAccount(ctx context.Context, account *store.Account) AccountResult {
	result := AccountResult{ID: account.ID, Name: account.Name}
	if account.Credit != nil {
		result.Credit = account.Credit.Total
	}

	// Refuse to sign an account the protocol does not cover, and say so plainly
	// instead of recording a failure that looks like a broken token.
	if reason := skipReason(account); reason != "" {
		result.Status = store.SigninSkipped
		result.Error = reason
		s.record(account.ID, func(target *store.Account) {
			target.SigninAt = time.Now()
			target.SigninStatus = store.SigninSkipped
			target.SigninError = reason
		})
		return result
	}

	cred := s.credOf(account)
	timeout := s.settings().SigninTimeout()
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	panel, err := s.client.SigninStatus(callCtx, cred)
	if err != nil {
		return s.fail(account, result, err)
	}
	result.Streak = panel.TodayDayNo

	if panel.ClaimedToday {
		// Already served today: still refresh the balance, because the sweep is
		// the only time we know the account is reachable.
		result.Status = store.SigninAlready
		result.Points = panel.TodayPoints
		credit := s.refreshCredit(ctx, account)
		if credit >= 0 {
			result.Credit = credit
		}
		s.record(account.ID, func(target *store.Account) {
			target.SigninAt = time.Now()
			target.SigninStatus = store.SigninAlready
			target.SigninStreak = panel.TodayDayNo
			target.SigninPoints = panel.TodayPoints
			target.SigninError = ""
			target.SigninPanel = panelOf(panel)
		})
		return result
	}

	claimCtx, cancelClaim := context.WithTimeout(ctx, timeout)
	defer cancelClaim()
	claim, err := s.client.SigninClaim(claimCtx, cred)
	if err != nil {
		return s.fail(account, result, err)
	}

	result.Points = claim.Points
	result.Streak = claim.DayNo
	if claim.IsDuplicate() {
		result.Status = store.SigninAlready
	} else {
		result.Status = store.SigninOK
	}
	credit := s.refreshCredit(ctx, account)
	if credit >= 0 {
		result.Credit = credit
	}

	s.record(account.ID, func(target *store.Account) {
		target.SigninAt = time.Now()
		target.SigninStatus = result.Status
		target.SigninStreak = claim.DayNo
		target.SigninPoints = claim.Points
		target.SigninError = ""
		target.SigninPanel = panelOf(panel)
		if !claim.IsDuplicate() && claim.Points > 0 {
			target.SigninTotal += int64(claim.Points)
		}
	})
	return result
}

// fail records an unsuccessful attempt. The attempt timestamp is written even
// on failure so the day counts as served: otherwise a sweep in which every
// account errored would be retried on the next tick, and a persistent upstream
// outage would turn into a request loop.
func (s *Service) fail(account *store.Account, result AccountResult, err error) AccountResult {
	result.Status = store.SigninFailed
	result.Error = truncate(err.Error(), 200)

	invalid := errors.Is(err, minimax.ErrInvalidCredential)
	s.record(account.ID, func(target *store.Account) {
		target.SigninAt = time.Now()
		target.SigninStatus = store.SigninFailed
		target.SigninError = result.Error
		if invalid {
			target.Status = store.StatusInvalid
			target.CooldownUntil = time.Time{}
			target.LastError = "签到：令牌已被上游拒绝"
		}
	})
	return result
}

// skipReason explains why an account cannot be checked in, or "" when it can.
func skipReason(account *store.Account) string {
	if !account.Enabled {
		return "账号已禁用"
	}
	if account.Region != store.RegionGlobal {
		return "仅支持国际站账号（国内站签到参数不同）"
	}
	if strings.TrimSpace(account.Token) == "" || account.UUID == "" || account.DeviceID == "" {
		return "缺少令牌或设备指纹"
	}
	return ""
}

// record applies a mutation to a stored account.
func (s *Service) record(id string, mutate func(*store.Account)) {
	s.store.SaveAccountState(id, mutate)
}

func panelOf(panel *minimax.SigninPanel) *store.SigninPanel {
	if panel == nil {
		return nil
	}
	out := &store.SigninPanel{Scene: panel.Scene, Days: make([]store.SigninDay, 0, len(panel.Days))}
	for _, day := range panel.Days {
		out.Days = append(out.Days, store.SigninDay{
			DayNo: day.DayNo, Points: day.Points, Status: day.Status, IsToday: day.IsToday,
		})
	}
	return out
}

// ------------------------------------------------------------------- credit

// refreshCredit reads the balance and stores it, returning the new total or -1
// when the read failed.
//
// The failure path is deliberately silent about status: a credit read failing
// says nothing about whether the token works, and marking the account invalid
// here would retire healthy accounts whenever the balance endpoint hiccups.
func (s *Service) refreshCredit(ctx context.Context, account *store.Account) int {
	if account.Region != store.RegionGlobal {
		return -1
	}
	callCtx, cancel := context.WithTimeout(ctx, s.settings().SigninTimeout())
	defer cancel()

	info, err := s.client.Credit(callCtx, s.credOf(account))
	if err != nil {
		return -1
	}
	s.record(account.ID, func(target *store.Account) {
		target.Credit = &store.Credit{
			Total: info.Total, Free: info.Free, Purchased: info.Purchased,
			PlanName: info.PlanName, PlanType: info.PlanType, SyncedAt: time.Now(),
		}
	})
	return info.Total
}

// RefreshCredit is the console's manual balance refresh.
func (s *Service) RefreshCredit(ctx context.Context, id string) (*store.Credit, error) {
	account, ok := s.store.AccountByID(id)
	if !ok {
		return nil, errors.New("账号不存在")
	}
	if account.Region != store.RegionGlobal {
		return nil, errors.New("余额接口仅支持国际站账号")
	}
	callCtx, cancel := context.WithTimeout(ctx, s.settings().SigninTimeout())
	defer cancel()

	info, err := s.client.Credit(callCtx, s.credOf(account))
	if err != nil {
		return nil, err
	}
	credit := &store.Credit{
		Total: info.Total, Free: info.Free, Purchased: info.Purchased,
		PlanName: info.PlanName, PlanType: info.PlanType, SyncedAt: time.Now(),
	}
	s.record(id, func(target *store.Account) { target.Credit = credit })
	return credit, nil
}

// creditLoop keeps the stored balances warm so the routing guard has something
// current to act on without waiting for the next sweep.
func (s *Service) creditLoop(ctx context.Context) {
	for {
		interval := s.settings().CreditRefresh()
		if interval <= 0 {
			// Disabled. Re-check periodically rather than returning, so turning
			// the poller back on in the console takes effect without a restart.
			if !sleep(ctx, time.Minute) {
				return
			}
			continue
		}
		if !sleep(ctx, interval) {
			return
		}
		if !s.settings().Signin.Enabled {
			continue
		}
		for _, account := range s.store.ListAccounts() {
			if ctx.Err() != nil {
				return
			}
			if !account.Enabled || account.Region != store.RegionGlobal {
				continue
			}
			s.refreshCredit(ctx, account)
			if !sleep(ctx, s.settings().SigninGap()) {
				return
			}
		}
	}
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "…"
}
