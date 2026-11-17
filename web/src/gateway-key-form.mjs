export const expiryDayOptions = [30, 90, 120];

export function parseConcurrencyInput(value) {
  const text = String(value ?? "").trim();
  if (text === "") return null;
  if (!/^(0|[1-9][0-9]{0,3})$/.test(text) || Number(text) > 1000)
    throw Error("invalid_concurrency_limit");
  return Number(text);
}

export function normalizeModelSelection(models) {
  return models.includes("*") ? ["*"] : [...new Set(models)];
}

export function toggleModelSelection(selected, model) {
  if (model === "*") return selected.includes("*") ? [] : ["*"];
  const specific = selected.filter((value) => value !== "*");
  return specific.includes(model)
    ? specific.filter((value) => value !== model)
    : [...specific, model];
}

export async function fetchAllModels(fetchPage) {
  const result = new Map();
  const limit = 500;
  for (let offset = 0; ; offset += limit) {
    const { data } = await fetchPage(`models?limit=${limit}&offset=${offset}`);
    for (const row of data) result.set(row.model, row);
    if (data.length < limit) return [...result.values()];
  }
}
