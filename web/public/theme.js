// Runs before first paint (classic script, CSP script-src 'self') so a pinned
// theme never flashes the system one.
(function () {
  var pref = null;
  try {
    pref = localStorage.getItem('bothy.theme');
  } catch (e) {}
  var mq = window.matchMedia('(prefers-color-scheme: dark)');
  function apply() {
    var t = pref === 'light' || pref === 'dark' ? pref : mq.matches ? 'dark' : 'light';
    document.documentElement.setAttribute('data-theme', t);
  }
  apply();
  mq.addEventListener('change', function () {
    try {
      pref = localStorage.getItem('bothy.theme');
    } catch (e) {}
    apply();
  });
})();
