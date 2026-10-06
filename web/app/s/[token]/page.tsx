"use client";
import { use, useRef, useState } from "react";
type Content = {
  type: string;
  title: string | null;
  text: string;
  expiresAt: string;
};
export default function PublicShare({
  params,
}: {
  params: Promise<{ token: string }>;
}) {
  const { token } = use(params);
  const [content, setContent] = useState<Content | null>(null),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(false);
  const started = useRef(false);
  async function open() {
    if (started.current) return;
    started.current = true;
    setBusy(true);
    try {
      const r = await fetch("/api/public/shares/" + encodeURIComponent(token), {
        cache: "no-store",
      });
      if (!r.ok) {
        setError(
          r.status === 404
            ? "Share not available"
            : "Unable to open share. Please try again later.",
        );
        return;
      }
      setContent(await r.json());
    } catch {
      setError("Unable to open share. Please try again later.");
    } finally {
      setBusy(false);
    }
  }
  return (
    <section className="public panel">
      <div className="eyebrow">A PRIVATE DELIVERY</div>
      {content ? (
        <>
          <h1>{content.title || "A message for you"}</h1>
          <pre className="textcontent">{content.text}</pre>
          <p className="muted">
            Expires {new Date(content.expiresAt).toLocaleString()}. This access
            has been counted.
          </p>
        </>
      ) : error ? (
        <>
          <h1>{error}</h1>
          <p>
            The link may have expired, been revoked, or reached its access
            limit.
          </p>
        </>
      ) : (
        <>
          <h1>A message for you.</h1>
          <p>
            Opening this share counts as one access. A one-time message will be
            unavailable after you leave or refresh.
          </p>
          <button className="button" disabled={busy} onClick={open}>
            {busy ? "Opening…" : "Open private message →"}
          </button>
        </>
      )}
    </section>
  );
}
