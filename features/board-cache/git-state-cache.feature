@ui @drvweb-030
Feature: The board caches per-attempt git state without ever showing a stale badge

  These scenarios are the executable statement of drvweb-030's acceptance criteria.
  The board re-renders every 3s, and the test-pyramid badge on each card used to start
  several `git` subprocesses per card per render — ~2.2s of a ~2.7s render over ~200
  mostly-Done attempts. A transparent in-memory cache now sits in front of those git
  reads. It validates each entry with cheap checks (branch refs read straight from
  disk, the worktree list, a once-per-render dirtiness sample of live worktrees, the
  overlay revision), so it changes how often git runs and never what is rendered.

  Each scenario drives ONE long-lived internal/web server over a real temp git repo,
  so the cache is warm between renders exactly as in a running `draiver webui`, and
  attaches a Playwright screenshot of the board.

  Scenario: A warm board render over many Done attempts is cheap and unchanged
    Given a board of 200 Done attempts whose worktrees were reclaimed but whose branches survive
    When the board is rendered cold and then warm
    Then the warm render shows every pyramid badge exactly as the cold render did
    And the warm render takes well under half a second

  Scenario: A commit on an attempt's branch turns its badge stale on the very next render
    Given a board with a Review attempt green at its branch tip
    And the board has been rendered so its git state is cached
    When a new commit lands on the attempt's branch
    Then the next board render shows the attempt's badge as stale

  Scenario: Dirtying a live worktree shows the badge dirty on the next render
    Given a board with a Review attempt green in its live worktree
    And the board has been rendered so its git state is cached
    When an uncommitted file appears in the attempt's worktree
    Then the next poll's board render shows the attempt's badge as dirty

  Scenario: Reclaiming the worktree falls back to the surviving branch
    Given a board with a Review attempt green in its live worktree
    And the board has been rendered so its git state is cached
    When the attempt commits again and its worktree is reclaimed, keeping the branch
    Then the next board render reads the branch tip and shows the attempt's badge as stale
