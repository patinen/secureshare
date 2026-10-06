export default function LoginButton() {
  // A regular form navigation reaches OAuth without client prefetching.
  return (
    <form action="/api/auth/github" method="get">
      <button className="button" type="submit">
        Continue with GitHub ↗
      </button>
    </form>
  );
}
