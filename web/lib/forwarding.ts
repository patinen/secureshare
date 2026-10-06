import { isIP } from "node:net";

// This switch attests that the upstream edge sanitizes XFF and cannot be bypassed.
// Next route handlers cannot independently authenticate the transport peer.
export function trustedChain(value: string | null, enabled: string | undefined): string | null {
  if (enabled !== "true" || !value || value.length > 4096) return null;
  const hops = value.split(",").map((part) => part.trim());
  if (hops.length > 32 || hops.some((ip) => ip.includes("%") || !isIP(ip))) return null;
  return hops.join(", ");
}

export function proxyHeaders(incoming: Headers, enabled: string | undefined): Headers {
  const outgoing = new Headers();
  for (const name of ["cookie", "content-type", "origin"]) {
    const value = incoming.get(name);
    if (value) outgoing.set(name, value);
  }
  const chain = trustedChain(incoming.get("x-forwarded-for"), enabled);
  if (chain) outgoing.set("x-forwarded-for", chain);
  return outgoing;
}
