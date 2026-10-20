# review-env/ttyd/motd.sh — shown to the reviewer on every login shell (sourced from
# /etc/profile.d). Points at the recall changes under review over the seeded DEMO-033.
cat <<'EOF'

┌─ draiver recall review env (drv-032) ───────────────────────────────────────┐
  Seeded ticket: DEMO-033   (attempts 0001-0004, the facts carry varied tags)

  The three changes under review:

    draiver recall DEMO-033              # header names the resolved attempt and,
                                         #   since it's the latest of 4, flags siblings
    draiver recall DEMO-033 --tags       # distinct tags, alphabetical, with counts
    draiver recall DEMO-033@0001         # the ticket@attempt address form ...
    draiver recall DEMO-033@0001 --tags  #   ... works in every mode
    draiver recall DEMO-033@0001 2 3     #   ... including positional seqs

  More to try:

    draiver recall DEMO-033 --all        # include superseded facts
    draiver recall DEMO-033 --chain 4    # the seemed-X -> now-Y history of a fact
    draiver recall DEMO-033 --type gotcha
    draiver attempt ls DEMO-033

  The board is writable — mutate it, then recall again:

    draiver log DEMO-033 --type note --tag demo "my first fact" --attempt 0004
    draiver recall DEMO-033 --tag demo

  (The '# DEMO-033@NNNN ...' header prints on stderr — stdout stays pipe-clean.)
└──────────────────────────────────────────────────────────────────────────────┘

EOF
