# State-Tree Prebuild — Operator Procedure

`znnd state-tree prebuild` builds the state tree ahead of an upgrade, against a frozen copy of the
chain database, while the currently-running (old) binary keeps the node online. On upgrade, the new
binary resumes the prebuilt tree from its persisted frontier and only has to fold the momentums
produced since the snapshot was taken, instead of the whole chain history.

This is the low-risk, manual alternative to the fully automatic background build (which folds the
whole history in a goroutine after the upgraded binary starts). Use it when you want to bound
upgrade-time downtime without waiting on the background build.

## Usage

```
znnd state-tree prebuild --data-dir <path-to-frozen-copy> [--statetree-out <path>] [--genesis <path>]
```

- `--data-dir` — path to a **frozen copy** of the node's data folder, i.e. a copy of the directory
  that contains the `nom` database (the one passed to `znnd --data`). This copy must be
  crash-consistent: take it as an atomic filesystem snapshot (an LVM, ZFS, or btrfs snapshot/clone)
  or as a plain copy taken **after a clean shutdown** of the node writing to it. Do not take a plain
  `cp -r` of a live leveldb — the tool opens the copy's `nom` database, and a copy of a database
  that was being written to while copied is not guaranteed to be internally consistent (files are
  copied at different points in time); such a copy can fail to open, or worse, open successfully
  against an inconsistent DB and produce a state tree that does not match the real chain.
- `--statetree-out` — where the built `statetree` database is written. Defaults to
  `<data-dir>/statetree`, matching where the node itself expects the state tree. It is rejected if
  it resolves to `--data-dir` itself or its `nom`/`cache` databases, and if it is a non-empty
  directory that is not already a state-tree database.
- `--genesis` — path to a genesis file, only needed if the node was started with a non-default
  genesis (mirrors `znnd --genesis`).

The command prints the frontier (height and hash) the built tree reached. It opens the frozen
copy's `nom` database read-only and does not touch its `cache` database at all, but opening a
leveldb — even read-only — still performs some physical I/O against it (lock file, log, journal
recovery); it does not change any chain data.

**Archive nodes.** This tool always builds a pruned state tree (it does not currently expose the
node's `StateTreeArchive` setting). Do not use it ahead of upgrading an archive node — the prebuilt
tree would be missing historical versions the node is configured to retain, with no error raised.

## Procedure

1. **Snapshot.** With the current binary still running, take a crash-consistent copy of its data
   folder as described above. This copy is `--data-dir` below.
2. **Prebuild.** Run `znnd state-tree prebuild --data-dir <snapshot> --statetree-out <live-data-dir>/statetree`
   against the snapshot. This can take as long as building the tree from scratch normally would —
   the point is that it runs against the copy, not the live node, so the live node stays up the
   whole time.
3. **Upgrade within the window.** Stop the old binary, install the new one, and start it against the
   live data folder — the same one the snapshot in step 1 was copied from. The node starts
   immediately: it finds the prebuilt `statetree` database already at (or near) its persisted
   frontier and folds the momentums produced between the snapshot and this restart in the
   background, the same background build that runs after any upgrade. Until that background build
   catches up to the chain frontier, state-root RPCs return an error, and once the state-root spork
   activates the node will not accept new momentums until the build completes — that degraded
   window is what "the N-hour window" below sizes, not the node's startup time.

The window between the snapshot and the upgrade should stay small enough that the degraded window
above stays acceptable, or the delta erodes the benefit of prebuilding at all.

## Sizing the window

The background build folds one version per momentum height at the measured mainnet rate of
**~12 momentums/sec**. The delta to fold is however many momentums landed between the snapshot and
the upgrade, at one momentum every 10 seconds (360 momentums/hour).

```
delta_momentums   = window_hours * 360
catch_up_seconds  = delta_momentums / 12
```

**Example.** Snapshot the data folder, then upgrade 6 hours later:

```
delta_momentums  = 6 * 360 = 2,160
catch_up_seconds = 2,160 / 12 = 180  (about 3 minutes)
```

So a 6-hour window between snapshot and upgrade costs about 3 minutes of background build time — far
below the time a from-scratch build takes, and far below the degraded window the node would
otherwise run in. Pick a window using your own tolerance for that degraded time: doubling the
window roughly doubles it (a 24-hour window costs about 12 minutes; a 1-hour window costs about 30
seconds).

## What a stale or corrupt prebuild does

A **missing** `statetree` database is not an error — the node just runs the full background build
from genesis, the outcome prebuilding exists to avoid, not a failure to react to.

A prebuilt tree that is from the wrong chain, or otherwise doesn't match the live chain's history at
the height it claims to be at, is rejected with an error telling you to remove the state-tree
database manually and let the node rebuild it from scratch. This is detected against the node's own
chain data, not by trusting the prebuilt database, so a bad prebuild does not get silently accepted —
but it is **not necessarily caught at startup**. For any delta beyond a few momentums, folding runs
on the background build described above, so the node starts normally and the mismatch surfaces
shortly after, when that build reaches the prebuilt tree's frontier. Don't read a clean startup as
confirmation the prebuild was good; watch for that error in the logs during the catch-up window.
