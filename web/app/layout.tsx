export const dynamic = "force-dynamic";
import type { Metadata } from "next";
import Link from "next/link";
import "./globals.css";
export const metadata: Metadata = {
  title: "SecureShare",
  description: "Temporary text. Shared on your terms.",
  robots: { index: false, follow: false },
  referrer: "no-referrer",
};
export default function Layout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en">
      <body>
        <header>
          <Link className="brand" href="/">
            ◈ SecureShare
          </Link>
          <span>PRIVATE BY DESIGN</span>
        </header>
        <main>{children}</main>
        <footer>
          Temporary text. Shared on your terms.{" "}
          <span>Text &amp; files</span>
        </footer>
      </body>
    </html>
  );
}
