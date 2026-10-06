// review-env/server.js — the tiny service draiver's own review environment stands up
// (drv-020). It binds the port draiver allocated and injected (DRAIVER_REVIEW_PORT) on
// 127.0.0.1 and greets with the parameterisation it was launched with: the instance
// slug, the port, and ENV_NAME — the repo-declared, web-UI-settable custom var
// (resolution #45). It writes its pid to server.pid so down.sh can stop it, and clears
// the pid on SIGTERM so a confirmed teardown leaves nothing behind.
const http = require('http');
const fs = require('fs');

const port = process.env.DRAIVER_REVIEW_PORT;
const instance = process.env.DRAIVER_REVIEW_INSTANCE || 'unknown';
const envName = process.env.ENV_NAME || 'unset';

const srv = http.createServer(function (req, res) {
  res.writeHead(200, { 'content-type': 'text/html' });
  res.end(
    '<!doctype html><title>draiver review env</title>' +
    '<h1>Review environment is up</h1>' +
    '<p>instance <code>' + instance + '</code> on port <code>' + port + '</code></p>' +
    '<p data-testid="env-name">ENV_NAME: <code>' + envName + '</code></p>'
  );
});

srv.listen(port, '127.0.0.1', function () {
  fs.writeFileSync('server.pid', String(process.pid));
});

process.on('SIGTERM', function () {
  try { fs.unlinkSync('server.pid'); } catch (e) {}
  process.exit(0);
});
