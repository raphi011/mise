// mise: small enhancements that need no server round trip. Every feature
// here has a plain HTML fallback; storage can be unavailable (private
// windows, blocked site data), so every access is guarded.

const RECENT_KEY = "mise.recentSearches";
const RECENT_MAX = 6;

function readRecent() {
  try {
    const v = JSON.parse(localStorage.getItem(RECENT_KEY) || "[]");
    return Array.isArray(v) ? v.filter((t) => typeof t === "string") : [];
  } catch {
    return [];
  }
}

function saveRecent(term) {
  term = (term || "").trim();
  if (!term) return;
  const list = [term, ...readRecent().filter((t) => t.toLowerCase() !== term.toLowerCase())].slice(0, RECENT_MAX);
  try {
    localStorage.setItem(RECENT_KEY, JSON.stringify(list));
  } catch {
    // Not stored; recent searches are a convenience.
  }
}

// Recent searches show while the search field is empty.
function renderRecent() {
  const box = document.querySelector("[data-recent]");
  const input = document.getElementById("search-input");
  if (!box || !input) return;
  const list = box.querySelector("[data-recent-list]");
  const terms = readRecent();
  list.replaceChildren(
    ...terms.map((term) => {
      const li = document.createElement("li");
      const b = document.createElement("button");
      b.type = "button";
      b.className = "chip";
      b.dataset.recentTerm = term;
      b.textContent = term;
      li.append(b);
      return li;
    }),
  );
  box.hidden = terms.length === 0 || input.value.trim() !== "";
}

// ---- Editor ----

// refreshRows updates which move buttons are usable after rows change.
function refreshRows(rows) {
  const items = [...rows.querySelectorAll(":scope > [data-row]")];
  items.forEach((li, i) => {
    li.querySelector('[data-row-move="up"]').disabled = i === 0;
    li.querySelector('[data-row-move="down"]').disabled = i === items.length - 1;
  });
}

// renumberSteps keeps step numbers, ids and labels in order.
function renumberSteps(steps) {
  [...steps.querySelectorAll(":scope > [data-step]")].forEach((li, i) => {
    const n = String(i + 1);
    li.querySelector("[data-step-n]").textContent = n;
    const relabel = (el) => (el.textContent = el.textContent.replace(/\d+/, n));
    const area = li.querySelector("textarea");
    area.id = "step-" + i;
    const label = li.querySelector("[data-step-label]");
    label.htmlFor = area.id;
    relabel(label);
    const timer = li.querySelector("input[name=step_timer]");
    timer.id = "step-timer-" + i;
    const timerLabel = li.querySelector("[data-step-timer-label]");
    timerLabel.htmlFor = timer.id;
    relabel(timerLabel);
    const rm = li.querySelector("[data-step-remove]");
    rm.value = "step-remove:" + i;
    rm.setAttribute("aria-label", rm.getAttribute("aria-label").replace(/\d+/, n));
  });
}

// Steps are added and removed in place; without JavaScript the buttons
// submit the form and the server does the same.
function setupSteps() {
  const fields = document.querySelector("[data-steps-fields]");
  if (!fields) return;
  const steps = fields.querySelector("[data-steps]");
  const template = document.getElementById("step-template");
  fields.addEventListener("click", (e) => {
    const add = e.target.closest("[data-step-add]");
    const rm = e.target.closest("[data-step-remove]");
    if (add) {
      e.preventDefault();
      const li = template.content.firstElementChild.cloneNode(true);
      steps.append(li);
      renumberSteps(steps);
      li.querySelector("textarea").focus();
    } else if (rm) {
      e.preventDefault();
      const li = rm.closest("[data-step]");
      const next = li.nextElementSibling || li.previousElementSibling;
      li.remove();
      renumberSteps(steps);
      (next ? next.querySelector("textarea") : fields.querySelector("[data-step-add]")).focus();
    }
  });
}

function setupRows() {
  const rows = document.querySelector("[data-rows]");
  if (!rows) return;
  refreshRows(rows);
  // Rows appended by the server (typed ingredient lines).
  new MutationObserver(() => refreshRows(rows)).observe(rows, { childList: true });
  rows.addEventListener("click", (e) => {
    const btn = e.target.closest("[data-row-move]");
    if (!btn) return;
    e.preventDefault();
    const li = btn.closest("[data-row]");
    switch (btn.dataset.rowMove) {
      case "up":
        if (li.previousElementSibling) li.previousElementSibling.before(li);
        break;
      case "down":
        if (li.nextElementSibling) li.nextElementSibling.after(li);
        break;
      case "remove": {
        const next = li.nextElementSibling || li.previousElementSibling;
        li.remove();
        (next ? next.querySelector("input[name=ing_name]") : document.getElementById("ing-new")).focus();
        return;
      }
    }
    refreshRows(rows);
    if (!btn.disabled) btn.focus();
    else li.querySelector("input[name=ing_name]").focus();
  });
}

function setupPhoto() {
  const box = document.querySelector("[data-photo]");
  if (!box) return;
  const input = box.querySelector("input[type=file]");
  const img = box.querySelector("[data-photo-preview]");
  const empty = box.querySelector("[data-photo-empty]");
  const error = box.querySelector("[data-photo-error]");
  let url = null;
  input.addEventListener("change", () => {
    const file = input.files && input.files[0];
    error.textContent = "";
    if (!file) return;
    if (file.size > Number(input.dataset.max)) {
      error.textContent = input.dataset.tooLarge;
      input.value = "";
      return;
    }
    if (url) URL.revokeObjectURL(url);
    url = URL.createObjectURL(file);
    img.src = url;
    img.hidden = false;
    if (empty) empty.hidden = true;
  });
}

// ---- Cook mode ----

// clock formats seconds as m:ss, or h:mm:ss from an hour (as ui.Clock).
function clock(seconds) {
  seconds = Math.max(0, Math.ceil(seconds));
  const h = Math.floor(seconds / 3600);
  const m = Math.floor(seconds / 60) % 60;
  const s = String(seconds % 60).padStart(2, "0");
  return h ? `${h}:${String(m).padStart(2, "0")}:${s}` : `${m}:${s}`;
}

// beep plays three short tones. The audio context is created on the tap
// that starts a timer, since browsers only allow sound after a gesture.
let audio = null;
function unlockAudio() {
  if (audio || !window.AudioContext) return;
  try {
    audio = new AudioContext();
  } catch {
    audio = null;
  }
}
function beep() {
  if (!audio) return;
  audio.resume().catch(() => {});
  for (let i = 0; i < 3; i++) {
    const t = audio.currentTime + i * 0.4;
    const osc = audio.createOscillator();
    const gain = audio.createGain();
    osc.frequency.value = 880;
    gain.gain.setValueAtTime(0.25, t);
    gain.gain.exponentialRampToValueAtTime(0.001, t + 0.3);
    osc.connect(gain).connect(audio.destination);
    osc.start(t);
    osc.stop(t + 0.3);
  }
}

// Cook mode shows one step at a time, keeps the screen awake while the
// page is visible, and runs step timers. Timers keep their end times in
// sessionStorage, so a reload or a detour to the recipe keeps them.
function setupCook() {
  const root = document.querySelector("[data-cook]");
  if (!root) return;
  const label = (name, vars) => Object.entries(vars).reduce((s, [k, v]) => s.replace("{" + k + "}", v), root.dataset[name]);
  const key = "mise.cook." + root.dataset.cook;
  const steps = [...root.querySelectorAll("[data-cook-step]")];
  const announce = root.querySelector("[data-cook-announce]");

  // Steps. The server renders the requested one visible.
  let current = Math.max(0, steps.findIndex((s) => !s.classList.contains("cook__step--off")));
  const goTo = (i) => {
    if (i < 0 || i >= steps.length) return;
    current = i;
    steps.forEach((s, j) => s.classList.toggle("cook__step--off", j !== i));
    render();
    const url = new URL(location.href);
    url.searchParams.set("step", String(i + 1));
    history.replaceState(null, "", url);
    steps[i].querySelector(".cook__count").focus();
  };

  // Wake lock: browsers drop it when the page is hidden; take it again
  // when it is visible.
  const badge = root.querySelector("[data-wake]");
  let lock = null;
  const wake = async () => {
    if (lock || !("wakeLock" in navigator) || document.visibilityState !== "visible") return;
    try {
      lock = await navigator.wakeLock.request("screen");
      badge.hidden = false;
      lock.addEventListener("release", () => {
        lock = null;
        badge.hidden = true;
      });
    } catch {
      // Not allowed here (e.g. plain HTTP); the badge stays hidden.
    }
  };

  // Timers: {step: {end}} running, {step: {left}} paused, {step: {done}}.
  let timers = {};
  try {
    timers = JSON.parse(sessionStorage.getItem(key) || "{}") || {};
  } catch {
    timers = {};
  }
  const save = () => {
    try {
      sessionStorage.setItem(key, JSON.stringify(timers));
    } catch {
      // Timers still run; they just don't survive a reload.
    }
  };
  const controls = [...root.querySelectorAll("[data-timer]")];
  const seconds = (box) => Number(box.dataset.seconds);
  const left = (i, box) => {
    const t = timers[i];
    if (!t) return seconds(box);
    if (t.done) return 0;
    if (t.end) return (t.end - Date.now()) / 1000;
    return t.left;
  };
  const render = () => {
    for (const box of controls) {
      const i = box.dataset.timer;
      const t = timers[i];
      const rest = left(i, box);
      let text;
      if (!t) text = label("cookStart", { t: clock(seconds(box)) });
      else if (t.done) text = label("cookDone", {});
      else if (t.end) text = label("cookPause", { t: clock(rest) });
      else text = label("cookResume", { t: clock(rest) });
      box.querySelector("[data-timer-label]").textContent = text;
      box.querySelector("[data-timer-reset]").hidden = !t || t.done;
      box.classList.toggle("cook-timer--running", !!(t && t.end));
      box.classList.toggle("cook-timer--done", !!(t && t.done));
      // Chips are for other steps' timers; the current step shows its own.
      const chip = root.querySelector(`[data-chip="${i}"]`);
      chip.hidden = !t || Number(i) === current;
      chip.classList.toggle("cook-chip--done", !!(t && t.done));
      chip.querySelector("[data-chip-time]").textContent = t && t.done ? root.dataset.cookChipDone : clock(rest);
    }
  };
  const tick = () => {
    let changed = false;
    for (const box of controls) {
      const i = box.dataset.timer;
      const t = timers[i];
      if (t && t.end && t.end <= Date.now()) {
        timers[i] = { done: true };
        changed = true;
        announce.textContent = label("cookAlert", { n: String(Number(i) + 1) });
        // Vibration needs a tap on the page first (not so after a reload).
        if (navigator.vibrate && navigator.userActivation?.hasBeenActive !== false) navigator.vibrate([300, 150, 300, 150, 300]);
        beep();
      }
    }
    if (changed) save();
    render();
  };

  root.addEventListener("click", (e) => {
    const go = e.target.closest("[data-cook-go]");
    if (go) {
      e.preventDefault();
      goTo(Number(go.dataset.cookGo));
      return;
    }
    const box = e.target.closest("[data-timer]");
    if (!box) return;
    const i = box.dataset.timer;
    const t = timers[i];
    if (e.target.closest("[data-timer-reset]")) {
      delete timers[i];
    } else if (e.target.closest("[data-timer-toggle]")) {
      unlockAudio();
      if (!t) timers[i] = { end: Date.now() + seconds(box) * 1000 };
      else if (t.done) delete timers[i];
      else if (t.end) timers[i] = { left: (t.end - Date.now()) / 1000 };
      else timers[i] = { end: Date.now() + t.left * 1000 };
    } else {
      return;
    }
    save();
    render();
  });
  // Done leaves cook mode; its timers go with it.
  root.querySelector("[data-cook-finish]")?.addEventListener("submit", () => {
    try {
      sessionStorage.removeItem(key);
    } catch {
      // Nothing to clear.
    }
  });
  document.addEventListener("visibilitychange", () => {
    wake();
    tick();
  });

  controls.forEach((box) => (box.hidden = false));
  wake();
  tick();
  setInterval(tick, 250);
}

// ---- Installing ----

const INSTALL_HINT_KEY = "mise.installHint";

// Chromium fires beforeinstallprompt when the app is installable; keeping
// the event lets the hint's Install button open the browser's dialog.
let installPrompt = null;
window.addEventListener("beforeinstallprompt", (e) => {
  e.preventDefault();
  installPrompt = e;
});
window.addEventListener("appinstalled", () => {
  installPrompt = null;
});

function isStandalone() {
  return window.matchMedia("(display-mode: standalone)").matches || navigator.standalone === true;
}

function isIOS() {
  // iPadOS reports itself as a Mac; touch points tell them apart.
  return /iPhone|iPad|iPod/.test(navigator.userAgent) || (navigator.platform === "MacIntel" && navigator.maxTouchPoints > 1);
}

function hintShown() {
  try {
    return localStorage.getItem(INSTALL_HINT_KEY) !== null;
  } catch {
    // Without storage the hint could not keep its promise to ask once.
    return true;
  }
}

// installHint opens the add-to-home-screen hint the server sent after a
// cleared cart, once per device and only on a phone browser that can
// install the app; otherwise it removes it.
function installHint(dialog) {
  let platform = null;
  if (!isStandalone() && !hintShown()) {
    if (isIOS()) platform = "ios";
    else if (installPrompt && window.matchMedia("(pointer: coarse)").matches) platform = "prompt";
  }
  if (!platform) {
    dialog.remove();
    return;
  }
  try {
    localStorage.setItem(INSTALL_HINT_KEY, new Date().toISOString());
  } catch {
    // Checked above; nothing to do.
  }
  dialog.dataset.platform = platform;
  dialog.querySelector("[data-install-prompt]").addEventListener("click", async () => {
    const prompt = installPrompt;
    installPrompt = null;
    dialog.close();
    if (prompt) await prompt.prompt();
  });
  dialog.showModal();
}

function registerServiceWorker() {
  if (!("serviceWorker" in navigator)) return;
  navigator.serviceWorker.register("/sw.js").catch(() => {
    // Not installable here (e.g. plain HTTP on a LAN address); the app
    // works the same.
  });
}

// ---- Meal plan: selection ----

// The plan keeps its selection in Datastar signals ($_sel holds "m:<meal>"
// or "r:<recipe>" keys, never both kinds); these helpers work out what a
// click selects, what a drag carries and what the Delete key removes.
// Selectable items carry their key in data-sel. Without JavaScript none of
// this exists: every meal has its menu, every suggestion its add sheet.

// selAnchor is the item a Shift-click selects a range from.
let selAnchor = null;

function shown(el) {
  return el.getClientRects().length > 0;
}

// editing reports whether a key press belongs to a field or an open dialog.
function editing(e) {
  if (e.target.closest?.("input, textarea, select, [contenteditable]")) return true;
  return document.querySelector("dialog[open]") !== null;
}

// dragImage shows a dragged selection as the grabbed item with a count.
// The copy loses its data attributes so Datastar leaves it alone.
function dragImage(e, el, count) {
  const ghost = el.cloneNode(true);
  for (const node of [ghost, ...ghost.querySelectorAll("*")]) {
    for (const a of [...node.attributes]) {
      if (a.name.startsWith("data-") || a.name === "id") node.removeAttribute(a.name);
    }
  }
  ghost.classList.add("drag-ghost");
  ghost.style.width = `${el.offsetWidth}px`;
  const badge = document.createElement("span");
  badge.className = "drag-ghost__count";
  badge.textContent = String(count);
  ghost.append(badge);
  document.body.append(ghost);
  e.dataTransfer.setDragImage(ghost, e.offsetX, e.offsetY);
  setTimeout(() => ghost.remove(), 0);
}

const plan = {
  // selecting reports whether a click selects rather than opens: with
  // Ctrl, Cmd or Shift, or any tap on a meal in select mode.
  selecting(e, el, mode) {
    if (mode) return el.dataset.sel.startsWith("m:");
    return e.metaKey || e.ctrlKey || e.shiftKey;
  },

  // select returns the selection after a click on el: Shift adds the range
  // from the last clicked item, in the order the page shows them; otherwise
  // the item is toggled. Selecting another kind starts over.
  select(e, el, sel) {
    const key = el.dataset.sel;
    const kind = key[0];
    const next = sel.filter((k) => k[0] === kind);
    if (e.shiftKey && selAnchor && selAnchor[0] === kind) {
      const keys = [...new Set([...document.querySelectorAll(`[data-sel^="${kind}:"]`)].filter(shown).map((n) => n.dataset.sel))];
      const a = keys.indexOf(selAnchor);
      const b = keys.indexOf(key);
      if (a >= 0 && b >= 0) {
        for (const k of keys.slice(Math.min(a, b), Math.max(a, b) + 1)) {
          if (!next.includes(k)) next.push(k);
        }
        return next;
      }
    }
    selAnchor = key;
    return next.includes(key) ? next.filter((k) => k !== key) : [...next, key];
  },

  // outside reports whether a click landed away from anything that acts
  // on the selection.
  outside(e) {
    return !e.target.closest("[data-sel], .plan-selbar, dialog, #toast, #sheet");
  },

  // prune drops keys whose items are no longer on the page.
  prune(sel) {
    if (sel.length === 0) return sel;
    const present = new Set([...document.querySelectorAll("[data-sel]")].map((n) => n.dataset.sel));
    const next = sel.filter((k) => present.has(k));
    return next.length === sel.length ? sel : next;
  },

  // meals returns the IDs of the selected meals.
  meals(sel) {
    return sel.filter((k) => k.startsWith("m:")).map((k) => k.slice(2));
  },

  // ids returns the IDs a drag carries.
  ids(drag) {
    return drag ? drag.slice(2).split(",") : [];
  },

  dragging(drag, key) {
    return drag !== "" && drag[0] === key[0] && drag.slice(2).split(",").includes(key.slice(2));
  },

  // dragStart returns what a drag from el carries: the selection of its
  // kind when el is in it, else el alone.
  dragStart(e, el, sel) {
    const key = el.dataset.sel;
    const kind = key[0];
    const group = sel.includes(key) ? sel.filter((k) => k[0] === kind) : [key];
    const drag = `${kind}:${group.map((k) => k.slice(2)).join(",")}`;
    e.dataTransfer.effectAllowed = "move";
    e.dataTransfer.setData("text/plain", drag);
    if (group.length > 1) dragImage(e, el, group.length);
    return drag;
  },

  // isDelete is Delete or Backspace (Mac keyboards label it delete) outside
  // fields and dialogs.
  isDelete(e) {
    return (e.key === "Delete" || e.key === "Backspace") && !e.altKey && !e.ctrlKey && !editing(e);
  },

  // deleteIDs are the meals the Delete key removes: the selected ones, or
  // the focused meal when none is selected.
  deleteIDs(e, sel) {
    const ids = plan.meals(plan.prune(sel));
    if (ids.length > 0) return ids;
    const card = e.target.closest?.('[data-sel^="m:"]');
    return card ? [card.dataset.sel.slice(2)] : [];
  },

  isEscape(e) {
    return e.key === "Escape" && !editing(e);
  },
};

// ---- Meal plan: recipe picker ----

// pickNames names the picked recipes for the picker's bar. Names are kept
// once seen, so recipes a search hides still have theirs.
const pickedNames = new Map();

function pickNames(ids) {
  for (const box of document.querySelectorAll("[data-pick-name]")) pickedNames.set(box.value, box.dataset.pickName);
  const names = ids.map((id) => pickedNames.get(id)).filter(Boolean);
  try {
    return new Intl.ListFormat(document.documentElement.lang || undefined, { type: "conjunction" }).format(names);
  } catch {
    return names.join(", ");
  }
}

window.mise = { installHint, plan, pickNames };

// ---- Delegated handlers ----

// Modal dialogs close on a click outside them. A click on the backdrop targets
// the <dialog> itself, so compare the point with its box; padding clicks land
// inside the box and stay open. The press must start on the backdrop too, so
// dragging a text selection out of a field does not dismiss the dialog.
let backdropPress = null;

function onBackdrop(e) {
  const d = e.target;
  if (!(d instanceof HTMLDialogElement) || !d.open) return null;
  const r = d.getBoundingClientRect();
  const inside = e.clientX >= r.left && e.clientX <= r.right && e.clientY >= r.top && e.clientY <= r.bottom;
  return inside ? null : d;
}

document.addEventListener("pointerdown", (e) => {
  backdropPress = onBackdrop(e);
});

document.addEventListener("click", (e) => {
  const d = onBackdrop(e);
  const pressed = backdropPress;
  backdropPress = null;
  // Keyboard-activated clicks have no coordinates (0, 0); ignore those.
  if (d && d === pressed && (e.clientX || e.clientY)) d.close();
});

document.addEventListener("click", async (e) => {
  const recent = e.target.closest("[data-recent-term]");
  if (recent) {
    const input = document.getElementById("search-input");
    input.value = recent.dataset.recentTerm;
    input.dispatchEvent(new Event("input", { bubbles: true }));
    input.focus();
    return;
  }
  const hit = e.target.closest("#search-results a");
  if (hit) {
    const input = document.getElementById("search-input");
    if (input) saveRecent(input.value);
    return;
  }
  const copy = e.target.closest("[data-copy]");
  if (copy) {
    const field = document.getElementById(copy.dataset.copy);
    const label = copy.querySelector("[aria-live]") || copy;
    try {
      await navigator.clipboard.writeText(field.value);
      label.textContent = copy.dataset.copied;
    } catch {
      // No clipboard access: select the link so it can be copied by hand.
      field.focus();
      field.select();
    }
  }
});

document.addEventListener("submit", (e) => {
  if (e.target.id === "search-form") saveRecent(e.target.elements.q.value);
});

document.addEventListener("input", (e) => {
  if (e.target.id === "search-input") renderRecent();
});

// Ctrl+Z or Cmd+Z presses the Undo of the toast on screen, outside fields.
document.addEventListener("keydown", (e) => {
  if (e.key.toLowerCase() !== "z" || !(e.metaKey || e.ctrlKey) || e.shiftKey || e.altKey) return;
  if (e.target.closest?.("input, textarea, select, [contenteditable]")) return;
  const undo = document.querySelector("#toast .toast__undo");
  if (!undo || getComputedStyle(undo.closest(".toast")).visibility === "hidden") return;
  e.preventDefault();
  undo.click();
});

// Panels marked data-open-wide (the import's activity) start collapsed
// on phones and open on wider screens.
function openWide() {
  if (!window.matchMedia("(min-width: 900px)").matches) return;
  for (const d of document.querySelectorAll("details[data-open-wide]")) d.open = true;
}

function init() {
  registerServiceWorker();
  openWide();
  renderRecent();
  setupRows();
  setupSteps();
  setupPhoto();
  setupCook();
}

if (document.readyState === "loading") {
  document.addEventListener("DOMContentLoaded", init);
} else {
  init();
}
