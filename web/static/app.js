// Everything works without this file; it only smooths a few edges.

// Ask before destructive posts (forms marked data-confirm), and stop
// double-submits from impatient clicks.
document.addEventListener("submit", (e) => {
  const form = e.target;
  if (form.dataset.confirm && !confirm(form.dataset.confirm)) {
    e.preventDefault();
    return;
  }
  const button = e.submitter;
  if (button) setTimeout(() => { button.disabled = true; });
});

// Ctrl/Cmd+Enter submits from a textarea.
document.addEventListener("keydown", (e) => {
  if (e.key === "Enter" && (e.ctrlKey || e.metaKey) && e.target.form && e.target.tagName === "TEXTAREA") {
    e.preventDefault();
    e.target.form.requestSubmit();
  }
});

// The Claude page fills the name and password typed there into its
// connection links. Neither leaves the page.
const setup = document.querySelector("[data-mcp-setup]");
if (setup) {
  // encodeURIComponent, plus the characters that would break shell quotes.
  const encode = (s) => encodeURIComponent(s).replace(/[!'()*]/g, (c) => "%" + c.charCodeAt(0).toString(16).toUpperCase());
  const slots = setup.querySelectorAll("[data-slot]");
  for (const slot of slots) slot.dataset.blank = slot.textContent;
  const fill = () => {
    for (const slot of slots) {
      const value = setup.querySelector(`input[name="${slot.dataset.slot}"]`).value;
      slot.textContent = value ? encode(value) : slot.dataset.blank;
    }
  };
  setup.addEventListener("input", fill);
  fill();
}
