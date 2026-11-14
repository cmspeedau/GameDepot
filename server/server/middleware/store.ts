import { systemConfig } from "../internal/config/sys-conf";

export default defineEventHandler((event) => {
  const path = getRequestURL(event).pathname;
  if (!path.startsWith("/store")) return;
  if (systemConfig.isStoreEnabled()) return;
  // Store disabled: redirect to the library homepage.
  return sendRedirect(event, "/library", 302);
});
