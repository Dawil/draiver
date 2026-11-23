// drvweb-029: copy the "fork this session" command to the clipboard.
//
// navigator.clipboard needs a secure context (https or localhost); the board is
// often served over plain http on a LAN address, where it is undefined or rejects.
// Fall back to document.execCommand('copy') from a hidden textarea. If both fail
// the command is still on the page in a selectable <code> for a manual copy.
(function () {
  function fallbackCopy(text) {
    var ta = document.createElement('textarea');
    ta.value = text;
    ta.setAttribute('readonly', '');
    ta.style.position = 'fixed';
    ta.style.top = '-1000px';
    ta.style.opacity = '0';
    document.body.appendChild(ta);
    ta.select();
    var ok = false;
    try { ok = document.execCommand('copy'); } catch (e) { ok = false; }
    document.body.removeChild(ta);
    return ok;
  }

  function copy(text) {
    if (navigator.clipboard && window.isSecureContext) {
      return navigator.clipboard.writeText(text).then(
        function () { return true; },
        function () { return fallbackCopy(text); });
    }
    return Promise.resolve(fallbackCopy(text));
  }

  function feedback(btn, ok) {
    var box = btn.closest('.fork-session');
    var status = box && box.querySelector('.fork-copied');
    if (!status) return;
    status.textContent = ok ? 'Copied' : 'Copy failed — select the command below';
    status.classList.toggle('fork-copied-failed', !ok);
    clearTimeout(btn._forkTimer);
    btn._forkTimer = setTimeout(function () { status.textContent = ''; }, 2000);
  }

  document.addEventListener('click', function (ev) {
    var btn = ev.target.closest && ev.target.closest('.fork-copy');
    if (!btn) return;
    copy(btn.getAttribute('data-command') || '').then(function (ok) { feedback(btn, ok); });
  });
})();
