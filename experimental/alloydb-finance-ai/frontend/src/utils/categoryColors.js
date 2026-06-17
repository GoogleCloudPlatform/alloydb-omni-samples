const PALETTE = [
  { bg: "#dbeafe", text: "#1e40af", bar: "#3B82F6" },
  { bg: "#dcfce7", text: "#166534", bar: "#22C55E" },
  { bg: "#fef3c7", text: "#92400e", bar: "#F59E0B" },
  { bg: "#fce7f3", text: "#9d174d", bar: "#EC4899" },
  { bg: "#ede9fe", text: "#5b21b6", bar: "#8B5CF6" },
  { bg: "#ccfbf1", text: "#115e59", bar: "#14B8A6" },
  { bg: "#fee2e2", text: "#991b1b", bar: "#EF4444" },
  { bg: "#e0f2fe", text: "#075985", bar: "#0EA5E9" },
  { bg: "#fef9c3", text: "#854d0e", bar: "#EAB308" },
  { bg: "#f3e8ff", text: "#6b21a8", bar: "#A855F7" },
  { bg: "#ffedd5", text: "#9a3412", bar: "#F97316" },
  { bg: "#d1fae5", text: "#065f46", bar: "#10B981" },
  { bg: "#e0e7ff", text: "#3730a3", bar: "#6366F1" },
  { bg: "#fce7f3", text: "#be185d", bar: "#EC4899" },
  { bg: "#f1f5f9", text: "#334155", bar: "#94A3B8" },
];

function hashIndex(str) {
  let h = 0;
  for (let i = 0; i < str.length; i++) h = (h * 31 + str.charCodeAt(i)) >>> 0;
  return h % PALETTE.length;
}

export function catPalette(category) {
  return PALETTE[hashIndex(category || "other")];
}

export function catBarColor(category) {
  return catPalette(category).bar;
}
