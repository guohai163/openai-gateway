export const expiryDayOptions = [30, 90, 120];

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
