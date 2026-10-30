// Polls the public status and reloads the page once the app is back.
(function () {
  "use strict";
  var root = document.getElementById("standby");
  if (!root) return;
  var d = root.dataset;
  var bar = document.getElementById("bar");
  var statusEl = document.getElementById("status");
  var etaEl = document.getElementById("eta");
  var loadedAt = Date.now();

  function duration(s) {
    return s < 60 ? s + " s" : Math.round(s / 60) + " min";
  }

  function label(st) {
    if (st.phase === "failed") return d.tError;
    if (st.phase === "sleeping") return d.tSleep;
    if (!st.can_wake && st.phase !== "waking") return d.tCap;
    if (st.phase === "waking" && st.steps > 0) {
      if (Date.now() - loadedAt > 10 * 60 * 1000) return d.tSlow;
      var n = Math.min(st.step + 1, st.steps);
      return d.tStep.replace("{n}", n).replace("{total}", st.steps);
    }
    return d.tStarting;
  }

  function poll() {
    fetch("/_standby/status", { cache: "no-store", credentials: "omit" })
      .then(function (r) {
        var type = r.headers.get("Content-Type") || "";
        // Anything but our JSON means the load balancer already sends us to the app.
        if (!r.ok || type.indexOf("application/json") !== 0) throw new Error("app");
        return r.json();
      })
      .then(function (st) {
        if (st.phase === "awake") {
          statusEl.textContent = d.tReady;
          window.location.reload();
          return;
        }
        // A sleep just finished: a fresh visit wakes the stack again.
        if (st.phase === "asleep" && d.visit === "true" && st.can_wake) {
          window.location.reload();
          return;
        }
        bar.value = st.steps > 0 ? Math.round((100 * st.step) / st.steps) : 0;
        statusEl.textContent = label(st);
        etaEl.textContent = d.showEta === "true" && st.eta_seconds ? d.tEta.replace("{s}", duration(st.eta_seconds)) : "";
        setTimeout(poll, 3000);
      })
      .catch(function (err) {
        if (err && err.message === "app") {
          window.location.reload();
          return;
        }
        setTimeout(poll, 5000);
      });
  }

  poll();
})();
