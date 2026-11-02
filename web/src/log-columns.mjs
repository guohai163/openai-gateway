const allColumns = [
  "created_at",
  "model",
  "owner_email",
  "status",
  "token_usage",
  "request_cost",
  "request_uuid",
  "archive_status",
  "ip",
  "duration_ms",
  "gateway_name",
  "upstream_name",
  "endpoint",
  "error_code",
  "kind",
];
const adminColumns = new Set([
  "owner_email",
  "archive_status",
  "upstream_name",
]);

/** @param {boolean} admin */
export function availableLogColumns(admin) {
  return allColumns.filter((column) => admin || !adminColumns.has(column));
}

/** Defaults use the table container width, accounting for sidebar and padding.
 * @param {number} width @param {boolean} admin
 */
export function defaultLogColumns(width, admin) {
  const selected = new Set(["created_at", "model", "status"]);
  if (width >= 520) selected.add("request_cost");
  if (width >= 760) selected.add("token_usage");
  if (width >= 1000) selected.add("duration_ms");
  if (width >= 1200) {
    selected.add("gateway_name");
    if (admin) selected.add("owner_email");
  }
  return availableLogColumns(admin).filter((column) => selected.has(column));
}

/** null means automatic; invalid or inaccessible saved choices are discarded.
 * @param {unknown} value @param {boolean} admin
 * @returns {string[] | null}
 */
export function normalizeLogColumnPreference(value, admin) {
  if (!Array.isArray(value)) return null;
  const columns = availableLogColumns(admin).filter((column) =>
    value.includes(column)
  );
  return columns.length ? columns : null;
}
