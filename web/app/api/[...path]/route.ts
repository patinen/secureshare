import { NextRequest } from "next/server";
export const dynamic = "force-dynamic";
async function proxy(
  request: NextRequest,
  context: { params: Promise<{ path: string[] }> },
) {
  const { path } = await context.params;
  const allowed =
    path[0] === "shares" ||
    path[0] === "auth" ||
    (path[0] === "public" && path[1] === "shares");
  if (!allowed) return new Response(null, { status: 404 });
  const origin = process.env.API_ORIGIN || "http://localhost:8080";
  const target = new URL("/" + path.map(encodeURIComponent).join("/"), origin);
  target.search = request.nextUrl.search;
  const headers = new Headers();
  for (const name of ["cookie", "content-type", "origin"]) {
    const value = request.headers.get(name);
    if (value) headers.set(name, value);
  }
  try {
    const upstream = await fetch(target, {
      method: request.method,
      headers,
      body: request.method === "GET" ? undefined : await request.text(),
      redirect: "manual",
      cache: "no-store",
      signal: AbortSignal.timeout(25000),
    });
    const outgoing = new Headers({
      "Cache-Control": "no-store",
      "Referrer-Policy": "no-referrer",
    });
    for (const name of ["content-type", "location"]) {
      const value = upstream.headers.get(name);
      if (value) outgoing.set(name, value);
    }
    for (const cookie of upstream.headers.getSetCookie())
      outgoing.append("set-cookie", cookie);
    return new Response(upstream.body, {
      status: upstream.status,
      headers: outgoing,
    });
  } catch {
    return Response.json(
      { error: "Service unavailable" },
      { status: 502, headers: { "Cache-Control": "no-store" } },
    );
  }
}
export { proxy as GET, proxy as POST, proxy as DELETE };
