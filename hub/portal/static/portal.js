// The portal's one script. It refreshes a page's content in place every few seconds (main's
// data-refresh), keeping open details, and the hold in its bar; and it shows or copies a
// token. The setup page doesn't refresh, so its tokens are fetched once. Its refreshes say so
// (X-Portal-Refresh), so they don't count as use: a page left open still signs you out after
// 12 hours. Without it, the pages work as they are, and show what was so when they loaded.
(() => {
  "use strict";

  const main = document.querySelector("main[data-refresh]");
  const chip = () => document.getElementById("hold-chip");

  if (main) {
    const every = Math.max(2, Number(main.dataset.refresh) || 5) * 1000;
    let busy = false;
    const refresh = async () => {
      if (busy || document.hidden) return;
      // Not while you select text or type in a form on it.
      const selection = window.getSelection();
      if (selection && !selection.isCollapsed && main.contains(selection.anchorNode)) return;
      const focused = document.activeElement;
      if (focused && main.contains(focused) && focused.matches("input, textarea, select")) return;
      busy = true;
      try {
        const response = await fetch(location.pathname, {
          headers: { "X-Portal-Refresh": "1" }, cache: "no-store", credentials: "same-origin",
        });
        if (response.status === 401 || response.redirected) {
          location.reload(); // signed out, or the account is new: the page itself says what's next
          return;
        }
        if (!response.ok) return;
        const page = new DOMParser().parseFromString(await response.text(), "text/html");
        const freshChip = page.getElementById("hold-chip");
        if (chip() && freshChip && !chip().isEqualNode(freshChip)) chip().replaceWith(freshChip);
        else if (chip() && !freshChip) chip().remove();
        else if (!chip() && freshChip) document.querySelector(".tabs").after(freshChip);
        const fresh = page.querySelector("main");
        if (!fresh) return;
        const open = new Set([...main.querySelectorAll("details[open][id]")].map((d) => d.id));
        fresh.querySelectorAll("details[id]").forEach((d) => { d.open = open.has(d.id); });
        // A part that hasn't changed stays as it is, so the map's live calls flow on.
        const before = new Map([...main.children].filter((c) => c.id).map((c) => [c.id, c]));
        const after = [...fresh.children].map((c) => {
          const kept = c.id && before.get(c.id);
          return kept && kept.isEqualNode(c) ? kept : c;
        });
        main.replaceChildren(...after);
      } catch {
        // The hub didn't answer this time; the next refresh tries again.
      } finally {
        busy = false;
      }
    };
    setInterval(refresh, every);
    document.addEventListener("visibilitychange", () => { if (!document.hidden) refresh(); });
  }

  // Tokens show as dots until you ask; Copy copies the config with the token either way.
  const copyText = (text) => {
    if (navigator.clipboard && window.isSecureContext) return navigator.clipboard.writeText(text);
    // Plain HTTP inside Nebula isn't a secure context, which the clipboard API needs.
    return new Promise((resolve, reject) => {
      const area = document.createElement("textarea");
      area.value = text;
      area.readOnly = true;
      area.className = "offscreen";
      document.body.append(area);
      area.select();
      const copied = document.execCommand("copy");
      area.remove();
      if (copied) resolve(); else reject(new Error("can't copy"));
    });
  };
  const flash = (button, text) => {
    const before = button.dataset.label || button.textContent;
    button.dataset.label = before;
    button.textContent = text;
    setTimeout(() => { button.textContent = before; }, 1500);
  };
  document.addEventListener("click", (event) => {
    const reveal = event.target.closest("[data-reveal]");
    if (reveal) {
      const box = reveal.closest(".snippet");
      const shown = box.classList.toggle("revealed");
      box.querySelectorAll(".secret").forEach((s) => {
        s.textContent = shown ? s.dataset.secret : "•".repeat(16);
      });
      reveal.textContent = shown ? "Hide token" : "Show token";
      return;
    }
    const copy = event.target.closest("[data-copy]");
    if (copy) {
      const pre = copy.closest(".snippet").querySelector("pre").cloneNode(true);
      pre.querySelectorAll(".secret").forEach((s) => { s.textContent = s.dataset.secret; });
      copyText(pre.textContent + "\n").then(() => flash(copy, "Copied"), () => flash(copy, "Can't copy"));
    }
  });
})();
