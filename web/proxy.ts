import { NextRequest, NextResponse } from "next/server";

export function proxy(request: NextRequest) {
  const nonce = Buffer.from(crypto.getRandomValues(new Uint8Array(16))).toString("base64");
  const dev = process.env.NODE_ENV === "development";
  const csp = `default-src 'self'; script-src 'self' 'nonce-${nonce}' 'strict-dynamic'${dev ? " 'unsafe-eval'" : ""}; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'${dev ? " ws: " : ""}; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'`;
  const incoming = new Headers(request.headers);
  incoming.set("x-nonce", nonce);
  incoming.set("Content-Security-Policy", csp);
  const response = NextResponse.next({ request: { headers: incoming } });
  response.headers.set("Content-Security-Policy", csp);
  response.headers.set("Cache-Control", "no-store");
  if (process.env.ENVIRONMENT === "production") response.headers.set("Strict-Transport-Security", "max-age=31536000");
  if (/^\/(s(?:\/|$)|api(?:\/|$)|dashboard(?:\/|$))/.test(request.nextUrl.pathname))
    response.headers.set("X-Robots-Tag", "noindex, nofollow, noarchive");
  return response;
}
export const config = { matcher: ["/((?!api(?:/|$)|_next/static|_next/image|favicon.ico).*)"] };
