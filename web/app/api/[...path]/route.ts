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
    path[0] === "audit" ||
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
  const maxBytes =
    path[0] === "shares" && path[1] === "file"
      ? 25 * 1024 * 1024 + 64 * 1024
      : 700000;
  let bytes = 0;
  const body = request.body?.pipeThrough(
    new TransformStream<Uint8Array, Uint8Array>({
      transform(chunk, controller) {
        bytes += chunk.byteLength;
        if (bytes > maxBytes) {
          controller.error(new Error("Request too large"));
          return;
        }
        controller.enqueue(chunk);
      },
    }),
  );
  try {
    const options: RequestInit & { duplex: "half" } = {
      method: request.method,
      headers,
      body:
        request.method === "GET" || request.method === "HEAD"
          ? undefined
          : body,
      duplex: "half",
      redirect: "manual",
      cache: "no-store",
      signal: AbortSignal.timeout(150000),
    };
    const upstream = await fetch(target, options);
    const outgoing = new Headers({
      "Cache-Control": "no-store",
      "Referrer-Policy": "no-referrer",
    });
    // Forward no browser-supplied client IP or request ID. Trusted edge
    // forwarding needs an authenticated proxy chain established in Phase 5.
    for (const name of [
      "content-type",
      "location",
      "retry-after",
      "x-request-id",
    ]) {
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
      {
        error:
          bytes > maxBytes
            ? "Upload exceeds size limit"
            : "Service unavailable",
      },
      {
        status: bytes > maxBytes ? 413 : 502,
        headers: { "Cache-Control": "no-store" },
      },
    );
  }
}
export { proxy as GET, proxy as POST, proxy as DELETE };
