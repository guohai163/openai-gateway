// Apply the saved appearance before styles load, including on the sign-in page.
// Keep this script external so it works with the gateway's script-src 'self' policy.
(() => {
  let preference;
  try {
    preference = localStorage.getItem("gateway-theme");
  } catch {}
  document.documentElement.dataset.theme =
    preference === "light" || preference === "dark"
      ? preference
      : matchMedia("(prefers-color-scheme: dark)").matches
        ? "dark"
        : "light";
})();
