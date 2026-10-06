import { test } from "node:test";
import assert from "node:assert/strict";
import { trustedChain, proxyHeaders } from "./forwarding.ts";
for (const [name, value, enabled, expected] of [
  ["default", "192.0.2.1", undefined, null],
  ["disabled spoof", "192.0.2.1", "false", null],
  ["IPv4", "192.0.2.1, 10.0.0.1", "true", "192.0.2.1, 10.0.0.1"],
  ["IPv6", "2001:db8::1, ::ffff:192.0.2.1", "true", "2001:db8::1, ::ffff:192.0.2.1"],
  ["bad IP", "192.0.2.999", "true", null],
  ["port", "192.0.2.1:80", "true", null],
  ["zone", "fe80::1%eth0", "true", null],
  ["empty hop", "192.0.2.1,", "true", null],
  ["too many", Array(33).fill("192.0.2.1").join(","), "true", null],
  ["too long", " ".repeat(4097), "true", null],
] as const) test(name, () => assert.equal(trustedChain(value, enabled), expected));

for (const enabled of [undefined, "false", "true"]) test(`BFF allowlist ${enabled}`, () => {
 const incoming = new Headers({cookie:"fixture=only",origin:"https://fixture.invalid","content-type":"application/octet-stream","x-forwarded-for":"192.0.2.1, 10.0.0.1","cf-connecting-ip":"192.0.2.99","x-real-ip":"192.0.2.99",forwarded:"for=192.0.2.99","x-request-id":"spoof","authorization":"must-drop","x-nonce":"spoof"});
 const outgoing = proxyHeaders(incoming,enabled);
 assert.equal(outgoing.get("x-forwarded-for"),enabled === "true" ? "192.0.2.1, 10.0.0.1" : null);
 for (const name of ["cf-connecting-ip","x-real-ip","forwarded","x-request-id","authorization","x-nonce"]) assert.equal(outgoing.get(name),null);
 for (const name of ["cookie","content-type","origin"]) assert.equal(outgoing.get(name),incoming.get(name));
});
