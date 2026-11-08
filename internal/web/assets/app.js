// Polls the public status and reloads the page once the app is back.
(function () {
  "use strict";
  var root = document.getElementById("standby");
  if (!root) return;
  var d = root.dataset;
  var bar = document.getElementById("bar");
  var statusEl = document.getElementById("status");
  var etaEl = document.getElementById("eta");
  var btn = document.getElementById("wake-btn");
  var loadedAt = Date.now();
  var DEFAULT_STEP_S = 40;
  var MIN_STEP_S = 20;
  var SOON_S = 5; // the status never estimates less: below it, stop counting
  var current = null; // the step being waited for
  var shown = 0;
  var etaEnd = 0;
  var HANDOVER_MS = 15000; // the load balancer needs two health checks, 5 s apart
  var awakeAt = 0;

  function duration(s) {
    if (s < 60) return s + " s";
    var rest = s % 60;
    return Math.floor(s / 60) + " min" + (rest ? " " + rest + " s" : "");
  }

  function label(st) {
    if (st.phase === "failed") return d.tError;
    if (st.phase === "sleeping") return d.tSleep;
    if (st.phase === "asleep" && st.can_wake) return d.tAsleep;
    if (!st.can_wake && st.phase !== "waking") return d.tCap;
    if (st.phase === "waking" && st.steps > 0) {
      if (Date.now() - loadedAt > 10 * 60 * 1000) return d.tSlow;
      return st.step >= st.steps - 1 ? d.tApp : d.tMachines;
    }
    return d.tStarting;
  }

  // A new step gets its share of the time still expected.
  function follow(st) {
    var now = Date.now();
    if (!current || current.step !== st.step || current.steps !== st.steps) {
      var share = st.eta_seconds / Math.max(st.steps - st.step, 1);
      current = { step: st.step, steps: st.steps, since: now, seconds: st.eta_seconds ? Math.max(share, MIN_STEP_S) : DEFAULT_STEP_S };
    }
    if (st.eta_seconds) {
      var end = now + st.eta_seconds * 1000;
      etaEnd = etaEnd ? Math.min(etaEnd, end) : end;
    }
  }

  function stopFollowing() {
    current = null;
    shown = 0;
    etaEnd = 0;
    bar.value = 0;
    etaEl.textContent = "";
  }

  // The bar fills the finished steps and creeps inside the current one: it keeps
  // moving through a long step and never announces a step that has not ended.
  function paint() {
    if (!current) return;
    var now = Date.now();
    var used = (now - current.since) / 1000 / current.seconds;
    var inside = used <= 1 ? 0.85 * used : 0.85 + 0.13 * (1 - 1 / used);
    var target = (100 * Math.min(current.step + inside, current.steps)) / current.steps;
    shown += Math.max(target - shown, 0) * 0.2;
    bar.value = shown;
    if (d.showEta !== "true" || !etaEnd) return;
    var left = Math.ceil((etaEnd - now) / 1000);
    etaEl.textContent = left > SOON_S ? d.tEta.replace("{s}", duration(left)) : d.tSoon;
  }

  function poll() {
    fetch("/_standby/status", { cache: "no-store", credentials: "omit" })
      .then(function (r) {
        var type = r.headers.get("Content-Type") || "";
        // No backend is ready yet: reloading would replace this page by the bare error.
        if (r.status >= 500) throw new Error("no backend");
        // Anything but our JSON means the load balancer already sends us to the app.
        if (!r.ok || type.indexOf("application/json") !== 0) throw new Error("app");
        return r.json();
      })
      .then(function (st) {
        if (!st.phase) throw new Error("app");
        if (st.phase === "awake") {
          current = null;
          bar.value = 100;
          etaEl.textContent = "";
          statusEl.textContent = d.tReady;
          // Still our status: the load balancer has not moved visitors to the app yet.
          awakeAt = awakeAt || Date.now();
          if (Date.now() - awakeAt >= HANDOVER_MS) {
            window.location.reload();
            return;
          }
          setTimeout(poll, 1000);
          return;
        }
        // A sleep just finished: a fresh visit wakes the stack again.
        if (st.phase === "asleep" && d.visit === "true" && st.can_wake) {
          window.location.reload();
          return;
        }
        if (btn) btn.disabled = !st.can_wake || (st.phase !== "asleep" && st.phase !== "failed");
        if (st.phase === "waking" && st.steps > 0) follow(st);
        else stopFollowing();
        statusEl.textContent = label(st);
        setTimeout(poll, 3000);
      })
      .catch(function (err) {
        if (err && err.message === "app") {
          window.location.reload();
          return;
        }
        setTimeout(poll, 2000);
      });
  }

  setInterval(paint, 200);
  poll();
})();
