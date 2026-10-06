"use client";
import { useEffect, useState, useRef } from "react";
import { useRouter } from "next/navigation";
import LoginButton from "../login-button";
import { fileSize } from "../format";
type Share = {
  id: string;
  type: "TEXT" | "FILE";
  fileName?: string;
  fileSize?: number;
  title: string | null;
  createdAt: string;
  expiresAt: string;
  redemptionCount: number;
  maxRedemptions: number | null;
  revokedAt: string | null;
};
async function api(path: string, init?: RequestInit) {
  const r = await fetch("/api" + path, { ...init, cache: "no-store" });
  if (!r.ok) {
    let message = "Request failed";
    try {
      message = (await r.json()).error || message;
    } catch {}
    throw new Error(message);
  }
  return r.status === 204 ? null : r.json();
}
function status(s: Share) {
  return s.revokedAt
    ? "Revoked"
    : new Date(s.expiresAt) <= new Date()
      ? "Expired"
      : s.maxRedemptions !== null && s.redemptionCount >= s.maxRedemptions
        ? "Exhausted"
        : "Active";
}
export default function Dashboard() {
  const router = useRouter();
  const fileRef = useRef<HTMLInputElement>(null);
  const [type, setType] = useState<"TEXT" | "FILE">("TEXT"),
    [file, setFile] = useState<File | null>(null);
  const [user, setUser] = useState<{ login: string } | null>(null),
    [ready, setReady] = useState(false),
    [list, setList] = useState<Share[]>([]),
    [error, setError] = useState(""),
    [url, setUrl] = useState(""),
    [copied, setCopied] = useState(false),
    [busy, setBusy] = useState(false),
    [title, setTitle] = useState(""),
    [text, setText] = useState(""),
    [hours, setHours] = useState("24"),
    [limit, setLimit] = useState("1");
  useEffect(() => {
    let alive = true;
    api("/auth/me")
      .then((u) => {
        if (alive) setUser(u);
        return api("/shares");
      })
      .then((s) => {
        if (alive) setList(s);
      })
      .catch((e) => {
        if (alive) setError(e.message);
      })
      .finally(() => {
        if (alive) setReady(true);
      });
    return () => {
      alive = false;
    };
  }, []);
  async function refresh() {
    setList(await api("/shares"));
  }
  async function create(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    setUrl("");
    setCopied(false);
    try {
      if (type === "TEXT" && new TextEncoder().encode(text).length > 102400)
        throw new Error("Text must be at most 100 KB.");
      const expiresAt = new Date(
        Date.now() + Number(hours) * 3600000,
      ).toISOString();
      let result;
      if (type === "FILE") {
        if (!file || file.size === 0 || file.size > 25 * 1024 * 1024)
          throw new Error("Select a non-empty file up to 25 MiB.");
        const form = new FormData();
        form.append("file", file);
        if (title) form.append("title", title);
        form.append("expiresAt", expiresAt);
        if (limit) form.append("maxRedemptions", limit);
        result = await api("/shares/file", { method: "POST", body: form });
      } else {
        result = await api("/shares", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            type: "TEXT",
            title: title || null,
            text,
            expiresAt,
            maxRedemptions: limit ? Number(limit) : null,
          }),
        });
      }
      setUrl(window.location.origin + "/s/" + result.token);
      setText("");
      setTitle("");
      setFile(null);
      if (fileRef.current) fileRef.current.value = "";
      await refresh();
    } catch (e) {
      setError(e instanceof Error ? e.message : "Unable to create share");
    } finally {
      setBusy(false);
    }
  }
  async function revoke(id: string) {
    setError("");
    try {
      await api("/shares/" + id, { method: "DELETE" });
      await refresh();
    } catch (e) {
      setError(e instanceof Error ? e.message : "Unable to revoke");
    }
  }
  if (!ready) return <p>Loading your workspace…</p>;
  if (!user)
    return (
      <section className="hero">
        <h1>Your private workspace.</h1>
        <p>Sign in to create and manage text shares.</p>
        {error && <p role="alert">{error}</p>}
        <LoginButton />
      </section>
    );
  return (
    <>
      <div className="pagehead">
        <div>
          <div className="eyebrow">YOUR WORKSPACE / {user.login}</div>
          <h1>Share with intention.</h1>
        </div>
        <button
          className="secondary"
          onClick={async () => {
            try {
              await api("/auth/logout", { method: "POST" });
              setUser(null);
              setUrl("");
              setList([]);
              router.replace("/");
            } catch {
              setError("Unable to sign out");
            }
          }}
        >
          Sign out
        </button>
      </div>
      {error && (
        <p className="error" role="alert">
          {error}
        </p>
      )}
      <div className="workspace">
        <section className="panel">
          <h2>{type === "TEXT" ? "New text share" : "New file share"}</h2>
          <p className="muted">A private share, with a clear end.</p>
          <form onSubmit={create}>
            <label>
              Share type
              <select
                value={type}
                onChange={(e) => setType(e.target.value as "TEXT" | "FILE")}
                disabled={busy}
              >
                <option value="TEXT">Text share</option>
                <option value="FILE">File share</option>
              </select>
            </label>
            <label>
              Title <span>optional</span>
              <input
                value={title}
                onChange={(e) => setTitle(e.target.value)}
                maxLength={150}
              />
            </label>
            {type === "TEXT" ? (
              <>
                <label>
                  Text
                  <textarea
                    required
                    rows={9}
                    value={text}
                    onChange={(e) => setText(e.target.value)}
                    placeholder="What would you like to share?"
                  />
                </label>
                <small>
                  {new TextEncoder().encode(text).length.toLocaleString()} /
                  102,400 bytes
                </small>
              </>
            ) : (
              <>
                <label>
                  File
                  <input
                    ref={fileRef}
                    type="file"
                    required
                    onChange={(e) => setFile(e.target.files?.[0] ?? null)}
                  />
                </label>
                <small>
                  {file
                    ? `${file.name} · ${fileSize(file.size)}`
                    : "Select one file · Maximum 25 MiB"}
                </small>
              </>
            )}
            <div className="fields">
              <label>
                Expires in
                <select
                  value={hours}
                  onChange={(e) => setHours(e.target.value)}
                >
                  <option value="1">1 hour</option>
                  <option value="24">24 hours</option>
                  <option value="168">7 days</option>
                  <option value="719">Within 30 days</option>
                </select>
              </label>
              <label>
                Access limit
                <input
                  type="number"
                  min="1"
                  max="1000"
                  value={limit}
                  onChange={(e) => setLimit(e.target.value)}
                  placeholder="Unlimited"
                />
              </label>
            </div>
            <button className="button" disabled={busy}>
              {busy ? "Creating…" : "Create secret link ↗"}
            </button>
          </form>
        </section>
        <section className="panel note">
          <div className="eyebrow">A KEY, NOT JUST A LINK</div>
          <h2>Give it to the right person.</h2>
          <p>This link contains the secret required to access the share.</p>
          <p>
            It appears only after creation. Save it now; refreshing this page
            will remove it.
          </p>
          {url ? (
            <div className="result">
              <label>
                Your secret link
                <input readOnly value={url} />
              </label>
              <button
                className="button"
                onClick={async () => {
                  try {
                    await navigator.clipboard.writeText(url);
                    setCopied(true);
                  } catch {
                    setError(
                      "Copy unavailable. Select and copy the link manually.",
                    );
                  }
                }}
              >
                {copied ? "Copied ✓" : "Copy link"}
              </button>
              <button className="quiet" onClick={() => setUrl("")}>
                Dismiss link
              </button>
            </div>
          ) : (
            <div className="placeholder">
              Your next secret link will appear here.
            </div>
          )}
        </section>
      </div>
      <section className="sharelist">
        <div className="pagehead">
          <h2>Your shares</h2>
          <button
            className="secondary"
            onClick={() =>
              refresh().catch(() => setError("Unable to refresh shares"))
            }
          >
            Refresh
          </button>
        </div>
        <p className="muted">
          Showing the 200 most recent shares. Secret links cannot be retrieved.
        </p>
        {list.length === 0 ? (
          <div className="panel">
            No shares yet. Start with a message above.
          </div>
        ) : (
          <div className="tablewrap">
            <table>
              <thead>
                <tr>
                  <th>Share</th>
                  <th>Type</th>
                  <th>Created</th>
                  <th>Expires</th>
                  <th>Accesses</th>
                  <th>Status</th>
                  <th>Action</th>
                </tr>
              </thead>
              <tbody>
                {list.map((s) => (
                  <tr key={s.id}>
                    <td>
                      {s.title || "Untitled share"}
                      {s.type === "FILE" && (
                        <div className="muted">
                          {s.fileName} · {fileSize(s.fileSize ?? 0)}
                        </div>
                      )}
                    </td>
                    <td>{s.type}</td>
                    <td>{new Date(s.createdAt).toLocaleString()}</td>
                    <td>{new Date(s.expiresAt).toLocaleString()}</td>
                    <td>
                      {s.redemptionCount} / {s.maxRedemptions ?? "∞"}
                    </td>
                    <td>
                      <span className={"badge " + status(s).toLowerCase()}>
                        {status(s)}
                      </span>
                    </td>
                    <td>
                      {!s.revokedAt && (
                        <button className="quiet" onClick={() => revoke(s.id)}>
                          Revoke
                        </button>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>
    </>
  );
}
