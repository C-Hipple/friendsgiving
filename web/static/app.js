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
