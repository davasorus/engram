// engram UI helpers. HTMX handles all the request/swap wiring declaratively
// (search, live preview, save); this file only holds:
//   1. rich rendering (mermaid + KaTeX) applied after any content swap
//   2. the Alpine component for [[wikilink]] autocomplete (caret-anchored
//      dropdown is the one bit HTMX alone doesn't do well)

// --- rich rendering ---------------------------------------------------------
function renderRich(root) {
  root = root || document;
  if (window.mermaid) {
    try {
      mermaid.initialize({ startOnLoad: false, theme: "dark" });
      root.querySelectorAll(".mermaid").forEach(function (el, i) {
        if (el.dataset.done) return;
        mermaid.render("mmd-" + Date.now() + "-" + i, el.textContent)
          .then(function (out) { el.innerHTML = out.svg; el.dataset.done = "1"; })
          .catch(function () {});
      });
    } catch (e) {}
  }
  if (window.katex) {
    root.querySelectorAll(".math-block").forEach(function (el) {
      try { katex.render(el.textContent, el, { displayMode: true, throwOnError: false }); } catch (e) {}
    });
    root.querySelectorAll(".math-inline").forEach(function (el) {
      try { katex.render(el.textContent, el, { displayMode: false, throwOnError: false }); } catch (e) {}
    });
  }
}
document.addEventListener("DOMContentLoaded", function () { renderRich(document); });
// Re-render after HTMX swaps content (e.g. live preview updates).
document.body && document.addEventListener("htmx:afterSwap", function (e) { renderRich(e.target); });

// --- Alpine component: wikilink autocomplete --------------------------------
// Registered via the alpine:init event so the component is guaranteed to be
// defined before Alpine scans the DOM for x-data — otherwise a load-order race
// can leave x-data="wikilinks()" undefined and the editor renders inert.
document.addEventListener("alpine:init", function () {
  window.Alpine.data("wikilinks", wikilinks);
});

function wikilinks() {
  return {
    open: false, items: [], active: 0, x: 0, y: 0, triggerPos: -1, status: "",
    save() {
      var id = document.querySelector(".editor").getAttribute("data-id") || "";
      var title = document.getElementById("ed-title").value;
      var body = document.getElementById("ed-body").value;
      var tags = document.getElementById("ed-tags").value.split(",").map(function (s) { return s.trim(); }).filter(Boolean);
      this.status = "saving…";
      var self = this;
      fetch("/api/notes", {
        method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ id: id, title: title, body: body, tags: tags })
      }).then(function (r) { return r.json(); }).then(function (n) {
        if (n.error) { self.status = "error: " + n.error; return; }
        location.href = "/note/" + n.id;
      }).catch(function () { self.status = "save failed"; });
    },
    onKeyup(e) {
      var ta = this.$refs.ta;
      var pos = ta.selectionStart;
      var m = ta.value.slice(0, pos).match(/\[\[([^\]\n]*)$/);
      if (!m) { this.open = false; return; }
      var frag = m[1].toLowerCase();
      this.triggerPos = pos - m[1].length;
      var self = this;
      fetch("/api/notes?limit=200").then(function (r) { return r.json(); }).then(function (notes) {
        self.items = (notes || []).filter(function (n) {
          return n.title.toLowerCase().indexOf(frag) !== -1;
        }).slice(0, 8);
        if (!self.items.length) { self.open = false; return; }
        self.active = 0;
        var r = ta.getBoundingClientRect();
        self.x = 20; self.y = 30;   // simple offset within the wrap
        self.open = true;
      });
    },
    onKeydown(e) {
      if (!this.open) return;
      if (e.key === "ArrowDown") { this.active = (this.active + 1) % this.items.length; e.preventDefault(); }
      else if (e.key === "ArrowUp") { this.active = (this.active - 1 + this.items.length) % this.items.length; e.preventDefault(); }
      else if (e.key === "Enter") { this.choose(this.items[this.active]); e.preventDefault(); }
      else if (e.key === "Escape") { this.open = false; }
    },
    choose(item) {
      var ta = this.$refs.ta;
      var pos = ta.selectionStart;
      var before = ta.value.slice(0, this.triggerPos);
      var after = ta.value.slice(pos);
      ta.value = before + item.title + "]]" + after;
      var np = (before + item.title + "]]").length;
      ta.setSelectionRange(np, np);
      this.open = false;
      ta.dispatchEvent(new Event("keyup"));   // refresh preview via htmx trigger
    }
  };
}
