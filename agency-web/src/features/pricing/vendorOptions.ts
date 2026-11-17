/** Vendor key used for models without any vendor metadata. */
export const OTHER_VENDOR_KEY = "__other__";

export type VendorFilterOption = {
  key: string;
  name: string;
  icon?: string;
  models: number;
};

export type VendorFilterRow = { vendor_name?: string; vendor_icon?: string };

/** Vendor key of a pricing row; models without metadata group together. */
export function vendorKeyOf(row: VendorFilterRow): string {
  const name = row.vendor_name?.trim();
  return name ? name : OTHER_VENDOR_KEY;
}

export function vendorNameOf(row: VendorFilterRow): string {
  return row.vendor_name?.trim() ?? "";
}

/** Count the models of every vendor so the tabs can show provider totals. */
export function buildVendorOptions(
  rows: VendorFilterRow[],
  otherLabel: string,
): VendorFilterOption[] {
  const options = new Map<string, VendorFilterOption>();
  for (const row of rows) {
    const key = vendorKeyOf(row);
    const current = options.get(key);
    if (current) {
      current.models += 1;
      continue;
    }
    options.set(key, {
      key,
      name: vendorNameOf(row) || otherLabel,
      icon: row.vendor_icon,
      models: 1,
    });
  }
  return [...options.values()].sort(
    (left, right) => right.models - left.models || left.name.localeCompare(right.name),
  );
}

/** Keep the selected tab when the vendor still exists after a refresh. */
export function activeVendorKey(
  selected: string,
  vendors: VendorFilterOption[],
): string {
  if (selected === "all") return selected;
  return vendors.some((vendor) => vendor.key === selected) ? selected : "all";
}

/** Search text matched against the model, its provider and its channels. */
export function matchesPricingSearch(
  query: string,
  row: { origin_model_name: string; vendor_name?: string; channelNames: string[] },
): boolean {
  const needle = query.trim().toLowerCase();
  if (!needle) return true;
  return (
    row.origin_model_name.toLowerCase().includes(needle) ||
    vendorNameOf(row).toLowerCase().includes(needle) ||
    row.channelNames.some((name) => name.toLowerCase().includes(needle))
  );
}
