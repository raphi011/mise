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
    const area = li.querySelector("textarea");
    area.id = "step-" + i;
    const label = li.querySelector("[data-step-label]");
    label.htmlFor = area.id;
    label.textContent = label.textContent.replace(/\d+/, n);
    const rm = li.querySelector("[data-step-remove]");
    rm.value = "step-remove:" + i;
    rm.setAttribute("aria-label", rm.getAttribute("aria-label").replace(/\d+/, n));
  });
}

// Phones edit the method as one step per line, wider screens one field per
// step. Only the visible variant is submitted; switching copies the steps.
function setupSteps() {
  const text = document.querySelector("[data-steps-text]");
  const fields = document.querySelector("[data-steps-fields]");
  if (!text || !fields) return;
  const steps = fields.querySelector("[data-steps]");
  const template = document.getElementById("step-template");
  const mq = window.matchMedia("(min-width: 900px)");
  let wide = null;

  const fieldValues = () => [...steps.querySelectorAll("textarea")].map((a) => a.value.replace(/\s*\n\s*/g, " ").trim()).filter(Boolean);
  const addStep = (value) => {
    const li = template.content.firstElementChild.cloneNode(true);
    li.querySelector("textarea").value = value;
    steps.append(li);
    return li;
  };
  const apply = () => {
    if (wide === mq.matches) return;
    if (wide === true) {
      text.value = fieldValues().join("\n");
    } else if (wide === false) {
      steps.replaceChildren();
      text.value.split("\n").map((s) => s.trim()).filter(Boolean).forEach(addStep);
      renumberSteps(steps);
    }
    wide = mq.matches;
    text.disabled = wide;
    steps.querySelectorAll("textarea").forEach((a) => (a.disabled = !wide));
  };
  apply();
  mq.addEventListener("change", apply);

  fields.addEventListener("click", (e) => {
    const add = e.target.closest("[data-step-add]");
    const rm = e.target.closest("[data-step-remove]");
    if (add) {
      e.preventDefault();
      const li = addStep("");
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

window.mise = { installHint };

function registerServiceWorker() {
  if (!("serviceWorker" in navigator)) return;
  navigator.serviceWorker.register("/sw.js").catch(() => {
    // Not installable here (e.g. plain HTTP on a LAN address); the app
    // works the same.
  });
}

// ---- Delegated handlers ----

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

function init() {
  registerServiceWorker();
  renderRecent();
  setupRows();
  setupSteps();
  setupPhoto();
}

if (document.readyState === "loading") {
  document.addEventListener("DOMContentLoaded", init);
} else {
  init();
}
