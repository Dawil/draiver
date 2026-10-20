#!/bin/sh
# review-env/ttyd/healthcheck.sh — a real HTTP GET of the injected port: 200 is green, a
# refused connection is red. draiver runs this green-expected on confirm-up (the URL is
# revealed only once it passes) and red-expected on confirm-down (still-green = a leak),
# closing the teardown loop deterministically (drv-020). Runs on the host, where up.sh
# already requires curl.
exec curl -fsS -o /dev/null "http://127.0.0.1:${DRAIVER_REVIEW_PORT}/"
