#!/bin/sh
# review-env/healthcheck.sh — a real HTTP GET of the injected port: 200 is green, a
# refused connection is red. draiver runs this green-expected on confirm-up (the URL is
# revealed only once it passes) and red-expected on confirm-down (still-green = a leak),
# closing the teardown loop deterministically (drv-020).
exec node -e "var h=require('http');h.get('http://127.0.0.1:'+process.env.DRAIVER_REVIEW_PORT+'/',function(r){process.exit(r.statusCode===200?0:1)}).on('error',function(){process.exit(1)})"
