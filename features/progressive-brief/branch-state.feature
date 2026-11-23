@drv-064 @cli @progressive-brief
Feature: The cold-start brief shows the branch state, read from git
  A resumed agent used to re-derive what its branch already held by re-reading diffs,
  or got it wrong with a two-dot diff against a moved base. The brief now carries a
  short, bounded Branch state block computed from the attempt's worktree: the branch,
  HEAD and merge-base, what is committed since the merge-base, what is uncommitted,
  and the commits the base gained since the cut — with a three-dot reminder.

  Scenario: After the base moves, the brief shows the branch's work and the base's new commits
    Given a drv-064 attempt whose worktree holds a commit and an uncommitted file
    And the drv-064 base has gained a commit since the cut
    When the drv-064 agent runs draiver brief
    Then the drv-064 brief shows the branch state block with the base's new commit

  Scenario: With no worktree, the brief has no branch state block
    Given a drv-064 attempt with no worktree
    When the drv-064 agent runs draiver brief
    Then the drv-064 brief has no branch state block
