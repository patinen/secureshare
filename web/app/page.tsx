import Link from "next/link";
import LoginButton from "./login-button";
export default function Home() {
  return (
    <section className="hero">
      <div className="eyebrow">A LITTLE LESS PERMANENT</div>
      <h1>
        Share a thought.
        <br />
        <em>Leave no open door.</em>
      </h1>
      <p>
        Create a private text link with an expiry and an optional access limit.
        Your recipient needs the link, never an account.
      </p>
      <LoginButton />
      <Link className="quiet" href="/dashboard">
        Open dashboard →
      </Link>
      <div className="features">
        <article>
          <b>01 / Secret links</b>
          <p>Only someone with your capability link can open the share.</p>
        </article>
        <article>
          <b>02 / Your limits</b>
          <p>Set a deadline or make your message available just once.</p>
        </article>
        <article>
          <b>03 / Close the door</b>
          <p>Revoke access whenever you need to.</p>
        </article>
      </div>
    </section>
  );
}
