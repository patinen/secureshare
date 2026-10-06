"use client";
import { use, useRef, useState } from "react";
import { fileSize } from "../../format";
type Content = {
  type: string;
  title: string | null;
  text?: string;
  fileName?: string;
  fileSize?: number;
  downloadUrl?: string;
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
      const r = await fetch(
        "/api/public/shares/" + encodeURIComponent(token) + "/redeem",
        {
          method: "POST",
          cache: "no-store",
        },
      );
      if (!r.ok) {
        setError(
          r.status === 404
            ? "Share not available"
            : "Unable to open share. Please try again later.",
        );
        return;
      }
      const result: Content = await r.json();
      setContent(result);
      if (result.type === "FILE" && result.downloadUrl) {
        const link = document.createElement("a");
        link.href = result.downloadUrl;
        link.download = result.fileName ?? "download";
        link.rel = "noreferrer";
        document.body.appendChild(link);
        link.click();
        link.remove();
      }
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
          <h1>
            {content.title ||
              (content.type === "FILE"
                ? "A file for you"
                : "A message for you")}
          </h1>
          {content.type === "TEXT" ? (
            <pre className="textcontent">{content.text}</pre>
          ) : (
            <>
              <p>
                {content.fileName} · {fileSize(content.fileSize ?? 0)}
              </p>
              <p className="muted">
                Your download has been requested. This download link expires
                within 60 seconds; it may be reused during that window.
              </p>
              <a
                className="button"
                href={content.downloadUrl}
                rel="noreferrer"
                download={content.fileName}
              >
                Download file again
              </a>
            </>
          )}
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
          <h1>A private share for you.</h1>
          <p>
            Opening this share counts as one access. A one-time share will be
            unavailable after you leave or refresh.
          </p>
          <button className="button" disabled={busy} onClick={open}>
            {busy ? "Opening…" : "Open private message →"}
          </button>
          <button className="secondary" disabled={busy} onClick={open}>
            {busy ? "Opening…" : "Download private file"}
          </button>
        </>
      )}
    </section>
  );
}
