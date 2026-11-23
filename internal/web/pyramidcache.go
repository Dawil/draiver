package web

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Dawil/draiver/internal/config"
	"github.com/Dawil/draiver/internal/overlay"
	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/worktree"
)

// pyramidcache.go is the transparent, in-memory memoizing layer in front of the
// pyramid badge's git reads (drvweb-030). The uncached gatherer
// (gatherPyramidStateErr) starts several `git` subprocesses per card per render;
// with ~200 cards re-rendered every 3s that kept an idle webui busy. The cache never
// changes WHAT is rendered — every value it serves was produced by the real gatherer
// — only HOW OFTEN the gatherer runs.
//
// Validity comes from a cheap fingerprint of the inputs the gatherer reads, rebuilt
// on every call without a per-card subprocess:
//
//   - the attempt branch's tip SHA, read from the ref files (gitrefs.go);
//   - whether a live managed worktree holds the branch, from a per-repo
//     `git worktree list` snapshot (re-read when <common>/worktrees changes, and at
//     most pyramidSnapTTL old);
//   - for a live worktree, `git status --porcelain` (DirtyAt) — the one subprocess
//     left on the warm path, sampled for all of a repo's live worktrees at once (in
//     parallel) once per render (dirtyAt). A dirty worktree is never served from the
//     cache, so the green badge's "clean at HEAD" guarantee (drvweb-021 #27) holds
//     exactly as before;
//   - the overlay revision (overlay repo + commit) the overlay pyramid would be read
//     at, resolved from the overlay repo's ref files, with the operator config
//     memoized on its file stat.
//
// An entry is reused only while its fingerprint is unchanged. A commit, a reclaimed
// worktree, or a moved overlay revision changes the fingerprint and the very next
// render recomputes — no TTL wait. TTLs are only a backstop: pyramidSnapTTL bounds
// the heuristic snapshots (worktree list, git dirs, config), pyramidEntryTTL bounds an
// entry whose fingerprint is exact SHAs, and pyramidErrTTL keeps a failed read only
// briefly. Anything the cheap check cannot read (reftable refs, a malformed ref)
// bypasses the cache and calls the gatherer directly.

const (
	// pyramidSnapTTL bounds the per-repo worktree-list / git-dir snapshots and the
	// config snapshot: a few seconds, one poll cycle. Refreshing them is O(repos).
	pyramidSnapTTL = 5 * time.Second
	// pyramidEntryTTL is the backstop for an entry validated by exact SHAs. It is
	// deliberately longer than a poll cycle: a short TTL here would re-run every card's
	// git reads every other tick, the cost the cache exists to remove.
	pyramidEntryTTL = 2 * time.Minute
	// pyramidErrTTL keeps a failed read (errPyramidRead) only until the next poll, so
	// a transient failure recovers on its own.
	pyramidErrTTL = 2 * time.Second
	// pyramidIdleEvict drops entries no render has asked for in this long — attempts
	// archived, deleted, or simply off every page — so a long-running webui does not
	// grow without bound. pyramidMaxEntries is the hard cap behind it.
	pyramidIdleEvict  = 10 * time.Minute
	pyramidMaxEntries = 8192
	// pyramidDirtyWindow is how long one dirtiness sample of a repo's live worktrees
	// serves: long enough to cover a single render of every card, far shorter than the
	// 3s poll, so each render samples afresh — "one check per repo per render".
	pyramidDirtyWindow = time.Second
	// pyramidDirtyParallel bounds the concurrent `git status` runs of one sample.
	pyramidDirtyParallel = 8
)

// pyramidKey identifies one attempt's cached state. Repo is part of the key because
// the gatherer reads from the attempt's recorded repo.
type pyramidKey struct{ Repo, Ticket, Attempt string }

// pyramidFP is the fingerprint an entry was computed under. Equal fingerprints mean
// the gatherer would read the same inputs: the same branch tip (Head, "" when the
// branch is absent), the same live checkout (WorktreePath, "" when none — a clean
// one, since a dirty one is never cached), and the same overlay revision.
type pyramidFP struct {
	Head         string
	WorktreePath string
	Overlay      string
}

type pyramidEntry struct {
	fp      pyramidFP
	st      pyramidState
	ok      bool
	expires time.Time
	used    time.Time
}

// pyramidFlight is one in-progress gatherer call, shared by concurrent callers with
// the same key and fingerprint (singleflight).
type pyramidFlight struct {
	fp   pyramidFP
	done chan struct{}
	st   pyramidState
	ok   bool
}

// repoSnap is a repo's cheap-check inputs that DO need a subprocess to build: its git
// dirs and which managed worktrees are live. It is rebuilt at most once per
// pyramidSnapTTL (or when <common>/worktrees changes), not per card.
type repoSnap struct {
	mu      sync.Mutex // serializes rebuilds of this one snapshot
	fetched time.Time
	epoch   uint64
	err     error
	dirs    gitDirs
	wtStat  fs.FileInfo
	live    map[worktree.Key]string // attempt → live checkout path
}

// dirtySample is one render's dirtiness reading of a repo's live worktrees, taken
// all at once (in parallel) by the first card that needs it and reused by the rest.
type dirtySample struct {
	mu    sync.Mutex // held while sampling, so concurrent renders share one sample
	at    time.Time
	epoch uint64
	dirty map[string]dirtyReading // by checkout path
}

type dirtyReading struct {
	dirty bool
	err   error
}

// dirsSnap memoizes an overlay repo's git dirs (no worktree list needed there).
type dirsSnap struct {
	mu      sync.Mutex
	fetched time.Time
	epoch   uint64
	err     error
	dirs    gitDirs
}

// cfgSnap memoizes the operator config on its resolved path and file stat.
type cfgSnap struct {
	fetched time.Time
	epoch   uint64
	path    string
	stat    fs.FileInfo
	cfg     config.Config
	err     error
}

type pyramidCache struct {
	inner func(ctx context.Context, a project.Attempt) (pyramidState, bool, error)
	refs  *refReader
	now   func() time.Time

	mu        sync.Mutex
	entries   map[pyramidKey]*pyramidEntry
	flights   map[pyramidKey]*pyramidFlight
	repos     map[string]*repoSnap
	dirty     map[string]*dirtySample // by repo
	overlays  map[string]*dirsSnap
	cfg       cfgSnap
	gen       uint64 // bumped by invalidate: an older flight must not store its result
	epoch     uint64 // bumped by invalidate: every snapshot is rebuilt on next use
	lastSweep time.Time

	// computes counts gatherer calls the cache made (misses); bypasses counts calls
	// that skipped the cache (dirty worktree, unreadable refs). Tests and the
	// benchmark read them to prove the warm path runs no gatherer.
	computes atomic.Int64
	bypasses atomic.Int64
}

func newPyramidCache(inner func(ctx context.Context, a project.Attempt) (pyramidState, bool, error)) *pyramidCache {
	return &pyramidCache{
		inner:    inner,
		refs:     newRefReader(),
		now:      time.Now,
		entries:  map[pyramidKey]*pyramidEntry{},
		flights:  map[pyramidKey]*pyramidFlight{},
		repos:    map[string]*repoSnap{},
		dirty:    map[string]*dirtySample{},
		overlays: map[string]*dirsSnap{},
	}
}

// errNoGitState is fingerprint's "nothing to show, and no git call needed to know
// it" verdict: the repo is not a usable git base, or the attempt has neither a live
// checkout nor a branch. The gatherer would return ok=false for both.
var errNoGitState = errors.New("pyramid cache: no git state")

// get is the cached pyramidGit. Its contract is the gatherer's: the same
// (pyramidState, ok) for the same on-disk git state.
func (c *pyramidCache) get(ctx context.Context, a project.Attempt) (pyramidState, bool) {
	if a.Repo == "" || !safeKeyPart(a.Ticket) || !safeKeyPart(a.ID) {
		// No repo is answered by the gatherer without touching git; an unsafe key is
		// rejected by it (worktree.Key.valid) — neither is worth caching.
		return c.bypass(ctx, a)
	}
	fp, err := c.fingerprint(ctx, a)
	if errors.Is(err, errNoGitState) {
		return pyramidState{}, false
	}
	if err != nil {
		return c.bypass(ctx, a)
	}
	key := pyramidKey{Repo: a.Repo, Ticket: a.Ticket, Attempt: a.ID}
	now := c.now()

	c.mu.Lock()
	c.maybeSweep(now)
	if e := c.entries[key]; e != nil && e.fp == fp && now.Before(e.expires) {
		e.used = now
		st, ok := e.st, e.ok
		c.mu.Unlock()
		return st, ok
	}
	if f := c.flights[key]; f != nil && f.fp == fp {
		c.mu.Unlock()
		select {
		case <-f.done:
			return f.st, f.ok
		case <-ctx.Done():
			return pyramidState{}, false
		}
	}
	f := &pyramidFlight{fp: fp, done: make(chan struct{})}
	c.flights[key] = f
	gen := c.gen
	c.mu.Unlock()

	// The shared computation must not die with whichever request happened to start
	// it, or every joined caller (and the cache) would see its cancellation.
	st, ok, gerr := c.inner(context.WithoutCancel(ctx), a)
	c.computes.Add(1)
	f.st, f.ok = st, ok

	c.mu.Lock()
	if c.flights[key] == f {
		delete(c.flights, key)
	}
	if c.gen == gen {
		ttl := pyramidEntryTTL
		if gerr != nil {
			ttl = pyramidErrTTL
		}
		done := c.now()
		c.entries[key] = &pyramidEntry{fp: fp, st: st, ok: ok, expires: done.Add(ttl), used: done}
	}
	c.mu.Unlock()
	close(f.done)
	return st, ok
}

func (c *pyramidCache) bypass(ctx context.Context, a project.Attempt) (pyramidState, bool) {
	c.bypasses.Add(1)
	st, ok, _ := c.inner(ctx, a)
	return st, ok
}

// invalidate drops an attempt's entries (in every repo) and forces every snapshot to
// be rebuilt on next use. A handler that changed git state (push, sync, promote,
// review env, mark done) calls it so the response it renders reflects the change.
func (c *pyramidCache) invalidate(ticket, att string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen++
	c.epoch++
	for k := range c.entries {
		if k.Ticket == ticket && k.Attempt == att {
			delete(c.entries, k)
		}
	}
}

// maybeSweep evicts idle entries about once a minute. Called with c.mu held.
func (c *pyramidCache) maybeSweep(now time.Time) {
	if now.Sub(c.lastSweep) < time.Minute && len(c.entries) <= pyramidMaxEntries {
		return
	}
	c.lastSweep = now
	for k, e := range c.entries {
		if now.Sub(e.used) > pyramidIdleEvict || !now.Before(e.expires) {
			delete(c.entries, k)
		}
	}
	if len(c.entries) > pyramidMaxEntries {
		clear(c.entries)
	}
}

// fingerprint builds the cheap validity key for a. errNoGitState short-circuits to
// ok=false; any other error means "cannot check cheaply" and the caller bypasses.
func (c *pyramidCache) fingerprint(ctx context.Context, a project.Attempt) (pyramidFP, error) {
	snap, err := c.repoSnapshot(ctx, a.Repo)
	if err != nil {
		// Not a git base (or unreadable): the gatherer's NewManager would fail the same
		// way. The snapshot holds the failure for pyramidSnapTTL, then retries.
		return pyramidFP{}, errNoGitState
	}
	key := worktree.Key{Ticket: a.Ticket, Attempt: a.ID}
	var fp pyramidFP
	head, found, err := c.refs.resolve(snap.dirs, "refs/heads/draiver/"+a.Ticket+"/"+a.ID)
	if err != nil {
		return pyramidFP{}, err
	}
	if found {
		fp.Head = head
	}
	if path, ok := snap.live[key]; ok {
		dirty, err := c.dirtyAt(ctx, a.Repo, path, snap.live)
		if err != nil {
			return pyramidFP{}, err
		}
		if dirty {
			// A dirty tree's working-copy pyramid can change under an unchanged HEAD:
			// never serve it from the cache.
			return pyramidFP{}, errors.New("pyramid cache: dirty worktree")
		}
		fp.WorktreePath = path
	} else if !found {
		return pyramidFP{}, errNoGitState
	}
	ov, err := c.overlayFingerprint(ctx, a)
	if err != nil {
		return pyramidFP{}, err
	}
	fp.Overlay = ov
	return fp, nil
}

// dirtyAt reports whether the live checkout at path is dirty, from the repo's current
// dirtiness sample. A sample older than pyramidDirtyWindow (or predating a write's
// invalidate) is retaken for every live worktree of the repo at once, in parallel —
// so a board render pays one parallel batch of `git status` per repo instead of one
// serial call per card. Dirtiness is never inferred from file stats: an in-place edit
// of a tracked file changes no directory or index mtime, and a cached "clean" for a
// changed tree would break the green badge's guarantee (drvweb-021 #27).
func (c *pyramidCache) dirtyAt(ctx context.Context, repo, path string, live map[worktree.Key]string) (bool, error) {
	c.mu.Lock()
	ds := c.dirty[repo]
	if ds == nil {
		ds = &dirtySample{}
		c.dirty[repo] = ds
	}
	epoch := c.epoch
	c.mu.Unlock()

	ds.mu.Lock()
	defer ds.mu.Unlock()
	now := c.now()
	if r, ok := ds.dirty[path]; ok && ds.epoch == epoch && now.Sub(ds.at) < pyramidDirtyWindow {
		return r.dirty, r.err
	}
	paths := make([]string, 0, len(live)+1)
	for _, p := range live {
		paths = append(paths, p)
	}
	if !slices.Contains(paths, path) {
		paths = append(paths, path)
	}
	// The sample is shared by every card of the render: one request's cancellation
	// must not turn it into errors for the others.
	sctx := context.WithoutCancel(ctx)
	readings := make([]dirtyReading, len(paths))
	sem := make(chan struct{}, pyramidDirtyParallel)
	var wg sync.WaitGroup
	for i, p := range paths {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; wg.Done() }()
			d, err := worktree.DirtyAt(sctx, p)
			readings[i] = dirtyReading{dirty: d, err: err}
		}()
	}
	wg.Wait()
	ds.dirty = make(map[string]dirtyReading, len(paths))
	for i, p := range paths {
		ds.dirty[p] = readings[i]
	}
	ds.at, ds.epoch = now, epoch
	r := ds.dirty[path]
	return r.dirty, r.err
}

// overlayFingerprint names the overlay revision the gatherer would read an overlay
// pyramid at — "<overlay repo>@<commit>" — or "" when no overlay is configured. It
// mirrors overlay.ReviewRevision (the attempt's overlay branch tip, else the overlay
// repo's HEAD) with ref-file reads instead of subprocesses. It is folded into every
// fingerprint whether or not the in-repo pyramid made the overlay moot: that only
// costs a recompute when the overlay moves, never a wrong badge.
func (c *pyramidCache) overlayFingerprint(ctx context.Context, a project.Attempt) (string, error) {
	cfg, err := c.config()
	if err != nil {
		return "", err
	}
	loc, ok, err := overlay.Resolve(a.Repo, cfg.RepoSettings(a.Repo).Overlay, cfg.OverlayDir)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", nil
	}
	dirs, err := c.overlayDirs(ctx, loc.Repo)
	if err != nil {
		return "", err
	}
	sha, found, err := c.refs.resolve(dirs, "refs/heads/draiver/"+a.Ticket+"/"+a.ID)
	if err != nil {
		return "", err
	}
	if !found {
		if sha, _, err = c.refs.resolve(dirs, "HEAD"); err != nil {
			return "", err
		}
	}
	return loc.Repo + "@" + sha, nil
}

// repoSnapshot returns a's repo snapshot, rebuilding it when stale. The returned
// value is a copy safe to read without locks.
func (c *pyramidCache) repoSnapshot(ctx context.Context, repo string) (repoSnap, error) {
	c.mu.Lock()
	snap := c.repos[repo]
	if snap == nil {
		snap = &repoSnap{}
		c.repos[repo] = snap
	}
	epoch := c.epoch
	c.mu.Unlock()

	snap.mu.Lock()
	defer snap.mu.Unlock()
	now := c.now()
	fresh := !snap.fetched.IsZero() && snap.epoch == epoch && now.Sub(snap.fetched) < pyramidSnapTTL
	if fresh && snap.err == nil {
		// A worktree added or removed (git worktree add/remove/prune) rewrites the
		// worktrees admin dir: re-list now rather than waiting out the TTL.
		fresh = sameFileState(snap.wtStat, statOrNil(filepath.Join(snap.dirs.Common, "worktrees")))
	}
	if !fresh {
		snap.fetched, snap.epoch = now, epoch
		snap.err = snap.rebuild(ctx, repo)
	}
	return repoSnap{dirs: snap.dirs, live: snap.live, err: snap.err}, snap.err
}

func (snap *repoSnap) rebuild(ctx context.Context, repo string) error {
	snap.live = nil
	dirs, err := readGitDirs(ctx, repo)
	if err != nil {
		return err
	}
	snap.dirs = dirs
	// Stat BEFORE listing: a change racing the list leaves a stat that no longer
	// matches, so the next call re-lists instead of trusting a stale list.
	snap.wtStat = statOrNil(filepath.Join(dirs.Common, "worktrees"))
	wm, err := worktree.NewManager(repo)
	if err != nil {
		return err
	}
	wts, err := wm.List(ctx)
	if err != nil {
		return err
	}
	live := make(map[worktree.Key]string, len(wts))
	for _, wt := range wts {
		if wt.Key != (worktree.Key{}) {
			live[wt.Key] = wt.Path
		}
	}
	snap.live = live
	return nil
}

func (c *pyramidCache) overlayDirs(ctx context.Context, repo string) (gitDirs, error) {
	c.mu.Lock()
	ds := c.overlays[repo]
	if ds == nil {
		ds = &dirsSnap{}
		c.overlays[repo] = ds
	}
	epoch := c.epoch
	c.mu.Unlock()

	ds.mu.Lock()
	defer ds.mu.Unlock()
	now := c.now()
	if ds.fetched.IsZero() || ds.epoch != epoch || now.Sub(ds.fetched) >= pyramidSnapTTL {
		ds.fetched, ds.epoch = now, epoch
		ds.dirs, ds.err = readGitDirs(ctx, repo)
	}
	return ds.dirs, ds.err
}

// config returns the operator config, re-read only when its path or file changes
// (or the snapshot ages out) — once per change instead of once per card.
func (c *pyramidCache) config() (config.Config, error) {
	path, err := config.Path("")
	if err != nil {
		return config.Config{}, err
	}
	st := statOrNil(path)
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	s := &c.cfg
	if s.fetched.IsZero() || s.epoch != c.epoch || s.path != path || !sameFileState(s.stat, st) ||
		now.Sub(s.fetched) >= pyramidSnapTTL {
		s.fetched, s.epoch, s.path, s.stat = now, c.epoch, path, st
		s.cfg, s.err = config.Load(path)
	}
	return s.cfg, s.err
}

// statOrNil stats path, mapping any failure (typically "absent") to nil so it
// compares with sameFileState.
func statOrNil(path string) fs.FileInfo {
	info, err := os.Stat(path)
	if err != nil {
		return nil
	}
	return info
}

// safeKeyPart mirrors worktree.Key.valid: a component that is safe to splice into a
// ref name and a path.
func safeKeyPart(v string) bool {
	return v != "" && v != "." && v != ".." && !strings.ContainsAny(v, "/\\")
}
