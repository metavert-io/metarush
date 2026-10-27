package localdb

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// UpgradeStep says what an upgrade does: from which version to which, and
// whether the release series changes (then MongoDB's feature compatibility
// version is moved on after the new server starts).
type UpgradeStep struct {
	From         string `json:"from"`
	To           string `json:"to"`
	SeriesChange bool   `json:"seriesChange"`
	// Finish: the server already runs To, but moving its data on to To's
	// release series didn't finish last time; only that step is left.
	Finish bool `json:"finish,omitempty"`
}

// ErrUpToDate means the database already runs the version this MetaRush sets up.
var ErrUpToDate = errors.New("the database on this computer is up to date")

// PlanUpgrade says what Upgrade would do, or why it can't.
func (s *Server) PlanUpgrade() (UpgradeStep, error) {
	st, ok := s.ReadState()
	if !ok || !st.Initiated {
		return UpgradeStep{}, ErrNotInstalled
	}
	if st.FeaturePending != "" {
		// The new server runs; its data still works as the older series.
		step := UpgradeStep{From: st.Version, To: st.Version, SeriesChange: true, Finish: true}
		if k := s.kernel(); KernelRefuses(st.Version, k) {
			return step, cantStart(st.Version, k)
		}
		return step, nil
	}
	// The version this MetaRush comes with, when this computer's Linux kernel
	// keeps it from running: the database stays where it is (D339).
	k := s.kernel()
	if cur := s.current().Version; KernelRefuses(cur, k) && Compare(cur, st.Version) > 0 {
		return UpgradeStep{From: st.Version, To: cur, SeriesChange: Series(st.Version) != Series(cur)},
			upgradeWaits(st.Version, cur, k)
	}
	to := s.pin().Version
	step := UpgradeStep{From: st.Version, To: to, SeriesChange: Series(st.Version) != Series(to)}
	switch c := Compare(to, st.Version); {
	case c == 0:
		return step, ErrUpToDate
	case c < 0:
		// An older MetaRush keeps using the newer server; it never goes back.
		return step, ErrUpToDate
	}
	if step.SeriesChange && !nextSeries(Series(st.Version), Series(to)) {
		return step, fmt.Errorf("the database can move only one release at a time (from %s to %s is more than one)", Series(st.Version), Series(to))
	}
	if !s.Available() {
		return step, ErrNoBuild
	}
	return step, nil
}

// nextSeries reports whether to is the release series right after from: a
// newer minor release of the same major one, or the next major release.
func nextSeries(from, to string) bool {
	fM, fm := versionParts(from)
	tM, tm := versionParts(to)
	return tM == fM && tm > fm || tM == fM+1 && tm == 0
}

// Upgrade moves the database to the version this MetaRush sets up: the new
// server is downloaded and checked first, the old one stops cleanly, the new
// one starts on the same data, and after a change of release series MongoDB's
// feature compatibility version moves on. adminURI is the administrator's
// address (MetaRush's own sign-in may only read and write its database).
// Only a person starts it (`metarush database upgrade`).
func (s *Server) Upgrade(ctx context.Context, adminURI string, progress func(done, total int64)) (UpgradeStep, error) {
	step, err := s.PlanUpgrade()
	if err != nil {
		return step, err
	}
	if !step.Finish {
		if err := s.Install(ctx, progress); err != nil {
			return step, err
		}
	}
	// One process at a time: nothing starts the old server while this one
	// moves the data to the new. Everything below runs under this one lock
	// (the *Locked steps; Start or Stop here would wait on it forever).
	unlock, err := s.lock(ctx)
	if err != nil {
		return step, err
	}
	defer unlock()
	if step.Finish {
		return step, s.finishFeatureVersionLocked(ctx, adminURI)
	}
	st, _ := s.ReadState()
	if k := s.kernel(); KernelRefuses(step.From, k) {
		// The version set up here can't start on this kernel (D339). Within
		// its release series the new one starts on the same data as it is;
		// moving to another series needs the old server to check the data first.
		if step.SeriesChange {
			return step, cantStart(step.From, k)
		}
	} else {
		if err := s.startLocked(ctx, st); err != nil {
			return step, err
		}
		fcv, err := s.featureVersion(ctx, adminURI)
		if err != nil {
			return step, err
		}
		if fcv != Series(step.From) {
			return step, plainErr("the database isn't ready to move on yet", fmt.Errorf("featureCompatibilityVersion %s, expected %s", fcv, Series(step.From)))
		}
		if err := s.stopLocked(ctx); err != nil {
			return step, err
		}
	}
	now := time.Now().UTC()
	st.UpgradedFrom, st.UpgradedAt, st.Version = st.Version, &now, step.To
	if err := s.writeState(st); err != nil {
		return step, err
	}
	if err := s.startLocked(ctx, st); err != nil {
		// Back to the version that ran before: the data was never touched by the new one.
		st.Version, st.UpgradedFrom, st.UpgradedAt = step.From, "", nil
		_ = s.writeState(st)
		if s.startLocked(ctx, st) == nil {
			return step, plainErr("the new version of the database didn't start, so the one before keeps running", err)
		}
		return step, err
	}
	_ = os.RemoveAll(s.versionDir(step.From))
	if step.SeriesChange {
		// Recorded first: a step that fails here is offered again
		// (PlanUpgrade's Finish) until it is done.
		st.FeaturePending = Series(step.To)
		if err := s.writeState(st); err != nil {
			return step, err
		}
		if err := s.finishFeatureVersionLocked(ctx, adminURI); err != nil {
			return step, err
		}
	}
	return step, nil
}

// finishFeatureVersionLocked moves the data on to the release series the
// server runs (State.FeaturePending), checks it, and records it; the caller
// holds the lock.
func (s *Server) finishFeatureVersionLocked(ctx context.Context, adminURI string) error {
	st, ok := s.ReadState()
	if !ok || st.FeaturePending == "" {
		return nil
	}
	if err := s.startLocked(ctx, st); err != nil {
		return err
	}
	if err := s.setFeatureVersion(ctx, adminURI, st.FeaturePending); err != nil {
		return err
	}
	if got, err := s.featureVersion(ctx, adminURI); err != nil || got != st.FeaturePending {
		return plainErr("the database runs the new version, but couldn't be moved on to it fully", fmt.Errorf("featureCompatibilityVersion %s, want %s: %v", got, st.FeaturePending, err))
	}
	st.FeatureVersion, st.FeaturePending = st.FeaturePending, ""
	return s.writeState(st)
}

func (s *Server) featureVersion(ctx context.Context, uri string) (string, error) {
	c, err := s.connect(ctx, uri)
	if err != nil {
		return "", err
	}
	defer c.Disconnect(context.Background())
	var res struct {
		FCV struct {
			Version string `bson:"version"`
		} `bson:"featureCompatibilityVersion"`
	}
	err = c.Database("admin").RunCommand(ctx, bson.D{{Key: "getParameter", Value: 1}, {Key: "featureCompatibilityVersion", Value: 1}}).Decode(&res)
	if err != nil {
		return "", plainErr("MetaRush couldn't ask the database which version it works as", err)
	}
	return res.FCV.Version, nil
}

func (s *Server) setFeatureVersion(ctx context.Context, uri, series string) error {
	c, err := s.connect(ctx, uri)
	if err != nil {
		return err
	}
	defer c.Disconnect(context.Background())
	cmd := bson.D{{Key: "setFeatureCompatibilityVersion", Value: series}}
	if major, _ := strconv.Atoi(strings.SplitN(series, ".", 2)[0]); major >= 7 {
		cmd = append(cmd, bson.E{Key: "confirm", Value: true})
	}
	if err := c.Database("admin").RunCommand(ctx, cmd).Err(); err != nil {
		return plainErr("the database runs the new version, but couldn't be moved on to it fully", err)
	}
	return nil
}

// Watch keeps the server running while ctx lasts (the daemon): every few
// seconds it checks that the server answers and starts it when it stopped.
// After three failed starts in ten minutes it tries only every five minutes
// and says so once.
func (s *Server) Watch(ctx context.Context, log func(string, ...any)) {
	var fails []time.Time
	quiet := false
	every := 10 * time.Second
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
		if s.PID() > 0 {
			if quiet {
				log("the database on this computer is running again")
			}
			fails, quiet, every = nil, false, 10*time.Second
			continue
		}
		if s.Answers(ctx) {
			// Something else answers on its port: never taken for it, never stopped.
			if !quiet {
				log("another program on this computer uses the database's port (%d); MetaRush can't start its database there", s.Port)
				quiet, every = true, 5*time.Minute
			}
			continue
		}
		log("the database on this computer stopped; starting it again")
		err := s.Start(ctx)
		if err == nil {
			fails = nil
			continue
		}
		if ctx.Err() != nil {
			return
		}
		now := time.Now()
		fails = append(fails, now)
		for len(fails) > 0 && now.Sub(fails[0]) > 10*time.Minute {
			fails = fails[1:]
		}
		log("the database on this computer didn't start: %v", err)
		if len(fails) >= 3 && !quiet {
			quiet, every = true, 5*time.Minute
			log("the database on this computer keeps failing to start; trying again every five minutes (metarush doctor says what is wrong)")
		}
	}
}
