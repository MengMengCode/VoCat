/** Accept a path already decoded once by URLSearchParams. Never decode it again. */
export function safeRedirect(value: string | null | undefined, origin = "https://vocat.invalid"): string {
  if (!value || value.length > 4096 || new TextEncoder().encode(value).length > 4096 || !value.startsWith("/") || value.startsWith("//")) return "/";
  if (/[\\\u0000-\u0020\u007f]/.test(value) || /%(?![0-9a-f]{2})/i.test(value)) return "/";
  const pathname = value.split(/[?#]/, 1)[0];
  // Encoded path separators, dot segments and another encoding layer cause
  // browser/server parsing differences. Query values are preserved verbatim.
  if (/%(?:2f|5c|2e|25|0[0-9a-f]|1[0-9a-f]|7f)/i.test(pathname)) return "/";
  try {
    const base = new URL(origin);
    const parsed = new URL(value, base);
    if (parsed.origin !== base.origin || parsed.pathname.startsWith("//")) return "/";
    // Inspect the path as the router sees it, including unreserved encoded
    // letters. The query and returned value are never decoded a second time.
    const routerPath = decodeURIComponent(parsed.pathname);
    if (/^\/(?:login(?:\/|$)|api\/auth(?:\/|$))/i.test(routerPath)) return "/";
    return value;
  } catch {
    return "/";
  }
}
