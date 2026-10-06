export default function LoginButton() {
  // Native navigation has no Next prefetch and is not a CSP form submission.
  // eslint-disable-next-line @next/next/no-html-link-for-pages -- OAuth API requires full browser navigation, not an RSC fetch.
  return <a className="button" href="/api/auth/github">Continue with GitHub ↗</a>;
}
